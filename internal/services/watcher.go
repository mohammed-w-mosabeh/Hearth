package services

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

// Topic is the WebSocket topic for units and their resource use.
const Topic = "services"

// Messages on Topic. "list" carries every unit and is sent when a client
// subscribes and whenever units change; "stats" carries resource use
// every StatsInterval.
type (
	ListMsg struct {
		Kind  string `json:"kind"` // "list"
		Units []Unit `json:"units"`
		// Errors names the managers that could not be read ("system",
		// "user") and why.
		Errors map[string]string `json:"errors,omitempty"`
	}
	StatsMsg struct {
		Kind  string          `json:"kind"`  // "stats"
		Stats map[string]Stat `json:"stats"` // by "<scope>/<unit>"
	}
)

// Bus is a closable D-Bus connection (dbusx.Conn in production).
type Bus interface {
	dbusx.Bus
	Close() error
}

// Dialer connects to one manager's bus.
type Dialer func(ctx context.Context) (Bus, error)

// Publisher is the subset of api.Hub the watcher needs.
type Publisher interface {
	Publish(topic string, data any) error
	Subscribers(topic string) int
	Watch(topic string) <-chan struct{}
}

// Config configures a Watcher.
type Config struct {
	System, User  Dialer // system bus, session bus
	Pub           Publisher
	Log           *slog.Logger
	CgroupRoot    string        // default /sys/fs/cgroup
	UID           int           // the user whose manager User reaches
	StatsInterval time.Duration // default 2 s
	Debounce      time.Duration // default 500 ms (ARCHITECTURE "Discovery")
	Retry         time.Duration // after a lost bus; default 5 s
}

// Watcher publishes units on Topic while it has subscribers.
type Watcher struct{ cfg Config }

// NewWatcher returns a Watcher; call Run.
func NewWatcher(cfg Config) *Watcher {
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if cfg.StatsInterval == 0 {
		cfg.StatsInterval = 2 * time.Second
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 500 * time.Millisecond
	}
	if cfg.Retry == 0 {
		cfg.Retry = 5 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Watcher{cfg: cfg}
}

// Run follows the managers while Topic has subscribers, until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	watch := w.cfg.Pub.Watch(Topic)
	for ctx.Err() == nil {
		if w.cfg.Pub.Subscribers(Topic) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-watch:
			}
			continue
		}
		if err := w.session(ctx, watch); err != nil && ctx.Err() == nil {
			w.cfg.Log.Warn("services: lost a bus, reconnecting", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(w.cfg.Retry):
			}
		}
	}
}

// mgr is one systemd manager (system or user) during a session.
type mgr struct {
	scope     string
	bus       Bus
	sigs      <-chan *dbus.Signal
	loaded    []listed
	files     map[string]string
	err       string // connecting or subscribing failed
	listErr   string // the last listing failed
	dirty     bool
	dirtyFile bool // unit files changed since the last listing started
	loading   bool // a unit file listing is running
}

// filesResult is a finished unit file listing.
type filesResult struct {
	m     *mgr
	files map[string]string
	err   error
}

func (w *Watcher) connect(ctx context.Context, scope string, dial Dialer) *mgr {
	m := &mgr{scope: scope}
	if dial == nil {
		m.err = "not configured"
		return m
	}
	bus, err := dial(ctx)
	if err != nil {
		m.err = "cannot connect to the " + scope + " manager"
		w.cfg.Log.Warn("services: connect", "scope", scope, "err", err)
		return m
	}
	m.bus = bus
	// Unit objects live under the manager's path; one match covers their
	// PropertiesChanged and the manager's UnitNew/UnitRemoved/Reloading.
	if m.sigs, err = bus.Watch(ctx, dbus.WithMatchPathNamespace(objPath)); err == nil {
		err = bus.Call(ctx, systemd, objPath, manager+".Subscribe", nil)
	}
	if err != nil {
		w.cfg.Log.Warn("services: subscribe", "scope", scope, "err", err)
		m.err = "cannot follow the " + scope + " manager"
	}
	m.dirty, m.dirtyFile = true, true
	w.reload(ctx, m)
	return m
}

func (w *Watcher) reload(ctx context.Context, m *mgr) {
	if m.bus == nil {
		return
	}
	if m.dirty {
		loaded, err := listUnits(ctx, m.bus)
		if err != nil {
			w.cfg.Log.Warn("services: units", "scope", m.scope, "err", err)
			m.listErr = "cannot list the " + m.scope + " units"
			return
		}
		m.loaded, m.dirty, m.listErr = loaded, false, ""
	}
}

// loadFiles lists unit files in the background: systemd stats every unit
// file for it, which takes over a second on the system manager, while
// ListUnits takes milliseconds.
func (w *Watcher) loadFiles(ctx context.Context, m *mgr, done chan<- filesResult) {
	if m.bus == nil || m.loading || !m.dirtyFile {
		return
	}
	m.loading, m.dirtyFile = true, false
	go func() {
		files, err := listFiles(ctx, m.bus)
		select {
		case done <- filesResult{m: m, files: files, err: err}:
		case <-ctx.Done():
		}
	}()
}

func (m *mgr) units() []Unit { return merge(m.scope, m.loaded, m.files) }

// session runs while Topic has subscribers; it returns nil when they are
// gone and an error when a bus connection drops.
func (w *Watcher) session(ctx context.Context, watch <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sys := w.connect(ctx, ScopeSystem, w.cfg.System)
	usr := w.connect(ctx, ScopeUser, w.cfg.User)
	for _, m := range []*mgr{sys, usr} {
		if m.bus != nil {
			defer func() { _ = m.bus.Close() }()
		}
	}

	var last ListMsg
	publishList := func(force bool) {
		msg := ListMsg{Kind: "list", Units: append(sys.units(), usr.units()...)}
		for _, m := range []*mgr{sys, usr} {
			if e := m.err + m.listErr; e != "" {
				if msg.Errors == nil {
					msg.Errors = map[string]string{}
				}
				msg.Errors[m.scope] = e
			}
		}
		if !force && reflect.DeepEqual(msg, last) {
			return
		}
		last = msg
		if err := w.cfg.Pub.Publish(Topic, msg); err != nil {
			w.cfg.Log.Warn("services: publish", "err", err)
		}
	}
	st := newStats(w.cfg.CgroupRoot, w.cfg.UID)
	publishStats := func() {
		_ = w.cfg.Pub.Publish(Topic, StatsMsg{Kind: "stats", Stats: st.sample(time.Now())})
	}
	publishList(true)
	publishStats()
	filesDone := make(chan filesResult)
	w.loadFiles(ctx, sys, filesDone)
	w.loadFiles(ctx, usr, filesDone)

	ticker := time.NewTicker(w.cfg.StatsInterval)
	defer ticker.Stop()
	var debounce <-chan time.Time
	signal := func(m *mgr, s *dbus.Signal) {
		m.dirty = true
		switch s.Name {
		case manager + ".UnitFilesChanged", manager + ".Reloading":
			m.dirtyFile = true
		}
		if debounce == nil {
			debounce = time.After(w.cfg.Debounce)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watch:
			if w.cfg.Pub.Subscribers(Topic) == 0 {
				return nil
			}
			publishList(true) // a new client needs the list
		case s, ok := <-sys.sigs:
			if !ok {
				return errors.New("system bus closed")
			}
			signal(sys, s)
		case s, ok := <-usr.sigs:
			if !ok {
				return errors.New("session bus closed")
			}
			signal(usr, s)
		case r := <-filesDone:
			r.m.loading = false
			if r.err != nil {
				w.cfg.Log.Warn("services: unit files", "scope", r.m.scope, "err", r.err)
			} else {
				r.m.files = r.files
			}
			w.loadFiles(ctx, r.m, filesDone) // changed again meanwhile
			publishList(false)
		case <-debounce:
			debounce = nil
			w.reload(ctx, sys)
			w.reload(ctx, usr)
			w.loadFiles(ctx, sys, filesDone)
			w.loadFiles(ctx, usr, filesDone)
			publishList(false)
		case <-ticker.C:
			publishStats()
		}
	}
}
