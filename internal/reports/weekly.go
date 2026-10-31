package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// SettingsStore keeps the weekly report's setting (store.DB in production).
type SettingsStore interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

const (
	weeklyKey   = "reports.weekly"
	weeklyEvery = 7 * 24 * time.Hour
	weeklyKeep  = 12 // saved reports; older ones are deleted
)

// savedRe matches the names of saved weekly reports; nothing else in the
// directory is listed or served.
var savedRe = regexp.MustCompile(`^hearth-week-\d{4}-\d{2}-\d{2}(-\d+)?\.html$`)

// WeeklyState is the weekly report setting.
type WeeklyState struct {
	Enabled bool  `json:"enabled"`
	Last    int64 `json:"last,omitempty"` // unix ms of the last saved report
}

// Saved is one saved report.
type Saved struct {
	Name string `json:"name"`
	Time int64  `json:"time"` // unix ms
	Size int64  `json:"size"`
}

// Weekly saves a machine report for the past week, once a week, while
// enabled, as a self-contained HTML file in Dir (mode 0600, SECURITY S20).
type Weekly struct {
	B        *Builder
	Settings SettingsStore
	Dir      string
	Log      *slog.Logger
	Clock    func() time.Time // replaced in tests

	mu       sync.Mutex // one Check at a time
	kickOnce sync.Once
	kick     chan struct{}
}

func (w *Weekly) kicks() chan struct{} {
	w.kickOnce.Do(func() { w.kick = make(chan struct{}, 1) })
	return w.kick
}

// Kick makes Run check now rather than at the next interval (the first
// report is due as soon as the weekly report is turned on).
func (w *Weekly) Kick() {
	select {
	case w.kicks() <- struct{}{}:
	default:
	}
}

func (w *Weekly) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// State returns the setting.
func (w *Weekly) State(ctx context.Context) (WeeklyState, error) {
	var st WeeklyState
	raw, ok, err := w.Settings.Setting(ctx, weeklyKey)
	if err != nil || !ok {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

func (w *Weekly) save(ctx context.Context, st WeeklyState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return w.Settings.SetSetting(ctx, weeklyKey, b)
}

// SetEnabled turns the weekly report on or off. Turning it on saves the
// first report at the next check.
func (w *Weekly) SetEnabled(ctx context.Context, on bool) (WeeklyState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := w.State(ctx)
	if err != nil {
		return st, err
	}
	st.Enabled = on
	return st, w.save(ctx, st)
}

// Toggle adapts a Weekly to what a settings import needs.
type Toggle struct{ W *Weekly }

// Enabled reports whether the weekly report is on.
func (t Toggle) Enabled(ctx context.Context) (bool, error) {
	st, err := t.W.State(ctx)
	return st.Enabled, err
}

// SetEnabled turns it on or off, and makes a report due at once if on.
func (t Toggle) SetEnabled(ctx context.Context, on bool) error {
	if _, err := t.W.SetEnabled(ctx, on); err != nil {
		return err
	}
	if on {
		t.W.Kick()
	}
	return nil
}

// Run checks every interval (an hour in production) until ctx ends.
func (w *Weekly) Run(ctx context.Context, interval time.Duration) {
	if w.Log == nil {
		w.Log = slog.Default()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("reports: weekly report", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.kicks():
		}
	}
}

// Check saves a report if one is due.
func (w *Weekly) Check(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := w.State(ctx)
	if err != nil || !st.Enabled {
		return err
	}
	now := w.now()
	if st.Last > 0 && now.Sub(time.UnixMilli(st.Last)) < weeklyEvery {
		return nil
	}
	m, err := w.B.Machine(ctx, "week")
	if err != nil {
		return err
	}
	page, err := MachineHTML(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(w.Dir, 0o700); err != nil {
		return fmt.Errorf("reports: %w", err)
	}
	name := "hearth-week-" + now.Local().Format("2006-01-02") + ".html"
	for i := 2; ; i++ { // a second report on the same day (re-enabled) keeps the first
		if _, err := os.Lstat(filepath.Join(w.Dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("hearth-week-%s-%d.html", now.Local().Format("2006-01-02"), i)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, name), page, 0o600); err != nil {
		return fmt.Errorf("reports: %w", err)
	}
	st.Last = now.UnixMilli()
	if err := w.save(ctx, st); err != nil {
		return err
	}
	w.prune()
	return nil
}

// List returns the saved reports, newest first.
func (w *Weekly) List() []Saved {
	entries, err := os.ReadDir(w.Dir)
	out := []Saved{}
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !savedRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Saved{Name: e.Name(), Time: info.ModTime().UnixMilli(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time != out[j].Time {
			return out[i].Time > out[j].Time
		}
		return out[i].Name > out[j].Name
	})
	return out
}

// Read returns a saved report by name.
func (w *Weekly) Read(name string) ([]byte, bool) {
	if !savedRe.MatchString(name) {
		return nil, false
	}
	p := filepath.Join(w.Dir, name)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() { // #nosec G703 -- name matches savedRe: no separator, no ".."
		return nil, false
	}
	b, err := os.ReadFile(p) // #nosec G304 G703 -- as above, in Hearth's own directory
	return b, err == nil
}

func (w *Weekly) prune() {
	saved := w.List()
	for _, s := range saved[min(len(saved), weeklyKeep):] {
		if err := os.Remove(filepath.Join(w.Dir, s.Name)); err != nil && w.Log != nil {
			w.Log.Warn("reports: cannot delete an old weekly report", "name", s.Name, "err", err)
		}
	}
}
