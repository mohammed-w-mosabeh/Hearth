package changes

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Store is what the tracker keeps (store.DB in production).
type Store interface {
	AddSnapshot(ctx context.Context, s store.Snapshot, changes []store.Change) (int64, error)
	Snapshots(ctx context.Context) ([]store.Snapshot, error)
	Snapshot(ctx context.Context, id int64) (store.Snapshot, error)
	LatestAutoSnapshot(ctx context.Context) (store.Snapshot, error)
	DeleteSnapshot(ctx context.Context, id int64) error
	PruneSnapshots(ctx context.Context, before time.Time) error
	ChangesBetween(ctx context.Context, from, to time.Time, sources []string, limit int) ([]store.Change, error)
	AuditBetween(ctx context.Context, from, to time.Time, limit int) ([]store.AuditRow, error)
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Reader reads one source's current state: keys and values.
type Reader func(ctx context.Context) (map[string]string, error)

// Config configures a Tracker. A nil reader leaves its source missing.
type Config struct {
	Store   Store
	Readers map[string]Reader // by source
	// LogDir holds dpkg.log; default /var/log.
	LogDir string
	Log    *slog.Logger
	Clock  func() time.Time
}

// keep is how long automatic snapshots and changes are kept.
const keep = 90 * 24 * time.Hour

// readTimeout bounds one source's reading.
const readTimeout = 20 * time.Second

// Tracker takes snapshots and builds the timeline.
type Tracker struct {
	cfg  Config
	dpkg *dpkgLog
	mu   sync.Mutex // one snapshot at a time
}

// New returns a Tracker.
func New(cfg Config) *Tracker {
	if cfg.LogDir == "" {
		cfg.LogDir = "/var/log"
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Tracker{cfg: cfg, dpkg: &dpkgLog{dir: cfg.LogDir}}
}

// Read captures the current state.
func (t *Tracker) Read(ctx context.Context) State {
	st := State{Sections: map[string]map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range Snapshotted {
		r := t.cfg.Readers[src]
		if r == nil {
			st.Missing = append(st.Missing, src)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, readTimeout)
			defer cancel()
			m, err := r(rctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.cfg.Log.Debug("changes: cannot read", "source", src, "err", err)
				st.Missing = append(st.Missing, src)
				return
			}
			if m == nil {
				m = map[string]string{}
			}
			st.Sections[src] = m
		}()
	}
	wg.Wait()
	sort.Strings(st.Missing)
	return st
}

// Take stores a snapshot. An automatic one is stored only when something
// changed since the last automatic one, with what changed (packages are
// left to dpkg's log); it returns stored false otherwise.
func (t *Tracker) Take(ctx context.Context, label string, auto bool) (snap store.Snapshot, stored bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.cfg.Clock()
	st := t.Read(ctx)
	var changes []store.Change
	if auto {
		prev, err := t.cfg.Store.LatestAutoSnapshot(ctx)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// The first: nothing to compare with.
		case err != nil:
			return snap, false, err
		default:
			old, err := decode(prev.Data)
			if err == nil && Same(old, st) {
				return prev, false, nil
			}
			if err == nil {
				changes = snapshotChanges(now, Diff(old, st))
			}
		}
	}
	data, err := encode(st)
	if err != nil {
		return snap, false, err
	}
	snap = store.Snapshot{Time: now, Label: label, Auto: auto, Data: data, Size: len(data)}
	if snap.ID, err = t.cfg.Store.AddSnapshot(ctx, snap, changes); err != nil {
		return snap, false, err
	}
	return snap, true, nil
}

// snapshotChanges turns a diff into timeline rows.
func snapshotChanges(now time.Time, diff []SectionDiff) []store.Change {
	var out []store.Change
	for _, d := range diff {
		if d.Source == SourcePackages || d.Missing {
			continue
		}
		for _, e := range d.Added {
			out = append(out, store.Change{Time: now, Source: d.Source, Kind: KindAdded, Subject: e.Key, After: e.After})
		}
		for _, e := range d.Removed {
			out = append(out, store.Change{Time: now, Source: d.Source, Kind: KindDeleted, Subject: e.Key, Before: e.Before})
		}
		for _, e := range d.Changed {
			out = append(out, store.Change{Time: now, Source: d.Source, Kind: KindChanged, Subject: e.Key, Before: e.Before, After: e.After})
		}
	}
	return out
}

// Run takes an automatic snapshot now and every interval, pruning old ones.
func (t *Tracker) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if _, _, err := t.Take(ctx, "", true); err != nil && ctx.Err() == nil {
			t.cfg.Log.Warn("changes: snapshot", "err", err)
		}
		if err := t.cfg.Store.PruneSnapshots(ctx, t.cfg.Clock().Add(-keep)); err != nil && ctx.Err() == nil {
			t.cfg.Log.Warn("changes: prune", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Event is one entry of the timeline.
type Event struct {
	Time    time.Time `json:"t"`
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	Before  string    `json:"before,omitempty"`
	After   string    `json:"after,omitempty"`
	// Hearth actions: what was done, from where and how it went.
	Action string `json:"action,omitempty"`
	Origin string `json:"origin,omitempty"`
	Result string `json:"result,omitempty"`
}

// Timeline returns events in [from, to) from the given sources (all if
// empty), newest first, at most limit. When there are more, next is the
// to of the following page: a page never ends in the middle of events
// that share a time (dpkg logs a whole upgrade in the same second).
func (t *Tracker) Timeline(ctx context.Context, from, to time.Time, sources []string, limit int) (events []Event, next *time.Time, err error) {
	want := map[string]bool{}
	for _, s := range sources {
		want[s] = true
	}
	all := len(want) == 0
	var out []Event
	if all || want[SourcePackages] {
		evs := t.dpkg.Events()
		// Oldest first: walk back from to.
		i := sort.Search(len(evs), func(i int) bool { return !evs[i].Time.Before(to) })
		for j := i - 1; j >= 0 && len(out) <= limit && !evs[j].Time.Before(from); j-- {
			out = append(out, evs[j])
		}
	}
	if all || want[SourceUnits] || want[SourceContainers] || want[SourceListeners] {
		cs, err := t.cfg.Store.ChangesBetween(ctx, from, to, sources, limit+1)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range cs {
			out = append(out, Event{Time: c.Time, Source: c.Source, Kind: c.Kind, Subject: c.Subject, Before: c.Before, After: c.After})
		}
	}
	if all || want[SourceHearth] {
		rows, err := t.cfg.Store.AuditBetween(ctx, from, to, limit+1)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			out = append(out, Event{Time: r.Time, Source: SourceHearth, Kind: KindAction, Subject: r.Target, Action: r.Action, Origin: r.Origin, Result: r.Result})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > limit {
		edge := out[limit].Time
		page := out[:limit]
		for len(page) > 0 && !page[len(page)-1].Time.After(edge) {
			page = page[:len(page)-1]
		}
		// Stores keep milliseconds: the next page's to includes edge.
		n := edge.Truncate(time.Millisecond).Add(time.Millisecond)
		if len(page) == 0 {
			// More than limit events at one time: show limit of them and
			// go on before that time (the rest of them are skipped).
			page, n = out[:limit], edge.Truncate(time.Millisecond)
		}
		out, next = page, &n
	}
	if out == nil {
		out = []Event{}
	}
	return out, next, nil
}

// SnapshotState returns snapshot id's state, or the current one for id 0.
func (t *Tracker) SnapshotState(ctx context.Context, id int64) (State, time.Time, error) {
	if id == 0 {
		return t.Read(ctx), t.cfg.Clock(), nil
	}
	s, err := t.cfg.Store.Snapshot(ctx, id)
	if err != nil {
		return State{}, time.Time{}, err
	}
	st, err := decode(s.Data)
	return st, s.Time, err
}
