package changes

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// What a chart marker can be.
const (
	MarkBoot      = "boot"
	MarkUpgrade   = "upgrade"
	MarkAlert     = "alert"
	MarkProfile   = "profile"
	MarkContainer = "container"
)

const (
	// maxMarkers bounds a reply; a chart cannot show more anyway.
	maxMarkers = 500
	// maxScan bounds what is read from each table for one reply.
	maxScan = 2000
	// maxMarkerSpan is the widest range asked for: a year of history.
	maxMarkerSpan = 400 * 24 * time.Hour
	// upgradeGap: upgrades closer than this are one run of apt.
	upgradeGap = 10 * time.Minute
	// bootSlack allows for the kernel's boot time moving with the clock.
	bootSlack = 2 * time.Minute
)

// Marker is one event on a chart.
type Marker struct {
	Time    int64  `json:"t"` // unix ms
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"` // system-derived: plain text
	Count   int    `json:"count,omitempty"`   // upgrades in one run, above 1
}

// markerAudit are the audited actions that make a marker.
var markerAudit = map[string]string{
	"profile.activate":   MarkProfile,
	"profile.deactivate": MarkProfile,
	"container.restart":  MarkContainer,
}

// upgradeRuns folds upgrades, oldest first, into one marker per run.
func upgradeRuns(evs []Event, from, to time.Time) []Marker {
	var out []Marker
	var last time.Time
	for _, e := range evs {
		if e.Kind != KindUpgraded || e.Time.Before(from) || !e.Time.Before(to) {
			continue
		}
		if n := len(out); n > 0 && e.Time.Sub(last) <= upgradeGap {
			out[n-1].Count++
			out[n-1].Subject = ""
		} else {
			out = append(out, Marker{Time: e.Time.UnixMilli(), Kind: MarkUpgrade, Subject: e.Subject, Count: 1})
		}
		last = e.Time
	}
	return out
}

// Markers returns the events to mark on charts of [from, to), newest first.
func (t *Tracker) Markers(ctx context.Context, from, to time.Time) ([]Marker, error) {
	out := upgradeRuns(t.dpkg.Events(), from, to)
	changed, err := t.cfg.Store.ChangesBetween(ctx, from, to, []string{SourceSystem, SourceContainers}, maxScan)
	if err != nil {
		return nil, err
	}
	for _, c := range changed {
		switch c.Kind {
		case KindBoot:
			out = append(out, Marker{Time: c.Time.UnixMilli(), Kind: MarkBoot})
		case KindRestarted:
			out = append(out, Marker{Time: c.Time.UnixMilli(), Kind: MarkContainer, Subject: c.Subject})
		}
	}
	alerts, err := t.cfg.Store.AlertEventsBetween(ctx, from, to, maxScan)
	if err != nil {
		return nil, err
	}
	for _, a := range alerts {
		out = append(out, Marker{Time: a.StartedAt, Kind: MarkAlert, Subject: a.RuleName})
	}
	rows, err := t.cfg.Store.AuditBetween(ctx, from, to, maxScan)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if kind, ok := markerAudit[r.Action]; ok && r.Result == "ok" {
			out = append(out, Marker{Time: r.Time.UnixMilli(), Kind: kind, Subject: r.Target})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	if len(out) > maxMarkers {
		out = out[:maxMarkers]
	}
	if out == nil {
		out = []Marker{}
	}
	return out, nil
}

// parseMarkerRange reads from and to, unix milliseconds.
func parseMarkerRange(q url.Values) (from, to time.Time, err error) {
	a, aerr := strconv.ParseInt(q.Get("from"), 10, 64)
	b, berr := strconv.ParseInt(q.Get("to"), 10, 64)
	if aerr != nil || berr != nil || a <= 0 || b <= a || b-a > maxMarkerSpan.Milliseconds() {
		return from, to, errors.New("from and to must be unix milliseconds, at most 400 days apart")
	}
	return time.UnixMilli(a), time.UnixMilli(b), nil
}

// handleMarkers serves GET /api/v1/markers?from=<ms>&to=<ms>.
func (t *Tracker) handleMarkers(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseMarkerRange(r.URL.Query())
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := t.Markers(r.Context(), from, to)
	if err != nil {
		t.cfg.Log.Warn("changes: markers", "err", err)
		httpError(w, http.StatusInternalServerError, "markers could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"markers": out})
}

// parseBootTime reads btime from /proc/stat.
func parseBootTime(stat []byte) (time.Time, bool) {
	for line := range bytes.SplitSeq(stat, []byte{'\n'}) {
		if v, ok := bytes.CutPrefix(line, []byte("btime ")); ok {
			n, err := strconv.ParseInt(string(bytes.TrimSpace(v)), 10, 64)
			return time.Unix(n, 0), err == nil && n > 0
		}
	}
	return time.Time{}, false
}

// recordBoot adds the boot to the timeline unless it is there already:
// hearthd starts many times within one boot.
func (t *Tracker) recordBoot(ctx context.Context, boot time.Time) error {
	seen, err := t.cfg.Store.ChangesBetween(ctx, boot.Add(-bootSlack), boot.Add(bootSlack), []string{SourceSystem}, 1)
	if err != nil || len(seen) > 0 {
		return err
	}
	return t.cfg.Store.AddChange(ctx, store.Change{Time: boot, Source: SourceSystem, Kind: KindBoot})
}

// noteBoot records this boot, once, from the kernel's boot time.
func (t *Tracker) noteBoot(ctx context.Context) {
	stat, err := os.ReadFile(filepath.Join(t.cfg.ProcRoot, "stat"))
	if err != nil {
		t.cfg.Log.Debug("changes: boot time", "err", err)
		return
	}
	if boot, ok := parseBootTime(stat); ok {
		if err := t.recordBoot(ctx, boot); err != nil && ctx.Err() == nil {
			t.cfg.Log.Warn("changes: record boot", "err", err)
		}
	}
}
