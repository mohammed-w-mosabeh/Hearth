package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/proxy"
	"github.com/mohammed-w-mosabeh/hearth/internal/reports"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// SettingUpdates keeps UpdateNotes across restarts.
const SettingUpdates = "stacks.updates"

// Settings is where UpdateNotes are kept (store.DB in production).
type Settings interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

// UpdateNote is what the owner's last update preview of an app found: the
// services a registry had a new digest for, against the images then
// running. SECURITY S21a asks registries only when the owner asks, so this
// is the only source of "needs an update"; it lapses once the spec or a
// running image changes (an update ran, or the app was redeployed).
type UpdateNote struct {
	SpecSHA256 string            `json:"spec_sha256"`
	Images     map[string]string `json:"images"` // service to image ID
	Services   []string          `json:"services"`
	At         int64             `json:"at"` // unix ms
}

// UpdateNotes holds each app's last update preview.
type UpdateNotes struct {
	Settings Settings // nil: kept in memory only
	Clock    func() time.Time

	mu     sync.Mutex
	m      map[string]UpdateNote
	loaded bool
}

func (n *UpdateNotes) load(ctx context.Context) {
	if n.loaded {
		return
	}
	n.loaded = true
	n.m = map[string]UpdateNote{}
	if n.Settings == nil {
		return
	}
	raw, ok, err := n.Settings.Setting(ctx, SettingUpdates)
	if err != nil || !ok {
		return
	}
	var m map[string]UpdateNote
	if json.Unmarshal(raw, &m) == nil {
		for k, v := range m {
			if spec.ValidName(k) {
				n.m[k] = v
			}
		}
	}
}

func (n *UpdateNotes) save(ctx context.Context) {
	if n.Settings == nil {
		return
	}
	if raw, err := json.Marshal(n.m); err == nil {
		_ = n.Settings.SetSetting(ctx, SettingUpdates, raw)
	}
}

// Note records a preview of info; no new digest clears the app's note.
func (n *UpdateNotes) Note(ctx context.Context, info broker.StackInfo, services []string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.load(ctx)
	if len(services) == 0 {
		if _, ok := n.m[info.Name]; !ok {
			return
		}
		delete(n.m, info.Name)
		n.save(ctx)
		return
	}
	now := time.Now
	if n.Clock != nil {
		now = n.Clock
	}
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	n.m[info.Name] = UpdateNote{SpecSHA256: info.SpecSHA256, Images: imagesOf(info), Services: sorted, At: now().UnixMilli()}
	n.save(ctx)
}

// Pending returns the services of info that the last preview found a new
// digest for, nil when there is none or it has lapsed.
func (n *UpdateNotes) Pending(ctx context.Context, info broker.StackInfo) []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.load(ctx)
	note, ok := n.m[info.Name]
	if !ok || note.SpecSHA256 != info.SpecSHA256 {
		return nil
	}
	now := imagesOf(info)
	for svc, id := range note.Images {
		if now[svc] != id {
			return nil
		}
	}
	return note.Services
}

func imagesOf(info broker.StackInfo) map[string]string {
	m := map[string]string{}
	for _, c := range info.Containers {
		m[c.Service] = c.ImageID
	}
	return m
}

// AppHealth is one managed app as Home, alerts, the change timeline and
// the weekly report see it.
type AppHealth struct {
	Name  string `json:"name"`
	State string `json:"state"` // running, partial, stopped or absent
	// Unhealthy lists containers that report unhealthy, restart, or
	// exited while the app is meant to run (state running or partial).
	Unhealthy []string           `json:"unhealthy"`
	Backup    *broker.BackupLast `json:"backup"`  // nil before a first backup
	Updates   []string           `json:"updates"` // from the last update preview
	Scheduled bool               `json:"scheduled"`
}

// BackupFailed tells whether the app's last backup failed.
func (a AppHealth) BackupFailed() bool { return a.Backup != nil && !a.Backup.OK }

// Overview reads every managed app: one stack.status, then one
// stack.backup.list per app. An app whose backups cannot be read is shown
// without them.
func Overview(ctx context.Context, b Broker, notes *UpdateNotes) ([]AppHealth, error) {
	var list []broker.StackInfo
	if err := b.Call(ctx, "stack.status", nil, &list); err != nil {
		return nil, err
	}
	out := make([]AppHealth, 0, len(list))
	for _, info := range list {
		a := AppHealth{Name: info.Name, State: info.State, Unhealthy: unhealthy(info), Updates: notes.Pending(ctx, info)}
		if a.Updates == nil {
			a.Updates = []string{}
		}
		var bl broker.BackupList
		if err := b.Call(ctx, "stack.backup.list", map[string]string{"name": info.Name}, &bl); err == nil {
			a.Backup, a.Scheduled = bl.Last, bl.Schedule != nil
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func unhealthy(info broker.StackInfo) []string {
	out := []string{}
	meant := info.State == "running" || info.State == "partial"
	for _, c := range info.Containers {
		switch {
		case c.Health == "unhealthy", c.State == "restarting", c.State == "dead",
			meant && c.State == "exited":
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// HandleHealth serves GET /api/v1/app-health.
func HandleHealth(b Broker, notes *UpdateNotes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := Overview(r.Context(), b, notes)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, list)
	}
}

// ReportApps is Overview as the machine report shows it.
func ReportApps(ctx context.Context, b Broker, notes *UpdateNotes) ([]reports.App, error) {
	list, err := Overview(ctx, b, notes)
	if err != nil {
		return nil, err
	}
	out := make([]reports.App, 0, len(list))
	for _, a := range list {
		r := reports.App{Name: a.Name, State: a.State, Unhealthy: a.Unhealthy, Scheduled: a.Scheduled, Updates: len(a.Updates)}
		if a.Backup != nil {
			r.Backup, r.BackupOK = a.Backup.At, a.Backup.OK
		}
		out = append(out, r)
	}
	return out, nil
}

// ProxyTargets is each managed app's reachable TCP ports, from its spec,
// for the app proxy (9.13).
func ProxyTargets(ctx context.Context, b Broker) (map[string][]int, error) {
	var list []broker.StackInfo
	if err := b.Call(ctx, "stack.status", nil, &list); err != nil {
		return nil, err
	}
	out := map[string][]int{}
	for _, info := range list {
		var sp spec.Spec
		if json.Unmarshal(info.Spec, &sp) == nil {
			out[info.Name] = proxy.AppPorts(sp)
		}
	}
	return out, nil
}

// Alert metrics fed by Watch.
const (
	MetricUnhealthy    = "app.unhealthy"
	MetricBackupFailed = "backup.failed"
)

// Observer receives metrics (alerts.Engine.Observe in production).
type Observer func(ctx context.Context, now time.Time, metric string, values map[string]float64)

// Watch feeds the app metrics every interval until ctx ends. A read that
// fails reports nothing, so incidents stay as they were.
func Watch(ctx context.Context, b Broker, notes *UpdateNotes, observe Observer, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		tick(ctx, b, notes, observe)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func tick(ctx context.Context, b Broker, notes *UpdateNotes, observe Observer) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, err := Overview(rctx, b, notes)
	if err != nil {
		return
	}
	sick, failed := map[string]float64{}, map[string]float64{}
	for _, a := range list {
		if len(a.Unhealthy) > 0 {
			sick[a.Name] = 1
		}
		if a.BackupFailed() {
			failed[a.Name] = 1
		}
	}
	now := time.Now()
	observe(ctx, now, MetricUnhealthy, sick)
	observe(ctx, now, MetricBackupFailed, failed)
}

// Snapshot is the change timeline's "apps" source: per app its state and
// spec, and its last backup, so deploys, restores and backups show there.
func Snapshot(ctx context.Context, b Broker) (map[string]string, error) {
	var list []broker.StackInfo
	if err := b.Call(ctx, "stack.status", nil, &list); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, info := range list {
		out[info.Name] = info.State + ", spec " + short(info.SpecSHA256)
		var bl broker.BackupList
		if err := b.Call(ctx, "stack.backup.list", map[string]string{"name": info.Name}, &bl); err == nil && bl.Last != nil {
			res := "ok"
			if !bl.Last.OK {
				res = "failed"
			}
			out[info.Name+" backup"] = bl.Last.At + " " + res
		}
	}
	return out, nil
}
