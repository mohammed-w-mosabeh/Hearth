package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

type memStore struct {
	mu       sync.Mutex
	autos    []store.Automation
	runs     []store.AutomationRun
	settings map[string]json.RawMessage
	audit    []store.AuditEntry
	approvs  []store.AutomationApproval
}

func (m *memStore) Automations(context.Context) ([]store.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Automation(nil), m.autos...), nil
}
func (m *memStore) SaveAutomationRun(_ context.Context, r store.AutomationRun) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID > 0 && int(r.ID) <= len(m.runs) {
		m.runs[r.ID-1] = r
		return r.ID, nil
	}
	r.ID = int64(len(m.runs) + 1)
	m.runs = append(m.runs, r)
	return r.ID, nil
}
func (m *memStore) GetAutomation(_ context.Context, id int64) (store.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.autos {
		if a.ID == id {
			return a, nil
		}
	}
	return store.Automation{}, store.ErrNotFound
}
func (m *memStore) GetAutomationRun(_ context.Context, id int64) (store.AutomationRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.runs) {
		return store.AutomationRun{}, store.ErrNotFound
	}
	return m.runs[id-1], nil
}
func (m *memStore) AddAutomationApproval(_ context.Context, a store.AutomationApproval) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ID = int64(len(m.approvs) + 1)
	a.State = store.ApprovalPending
	a.AutomationID = m.runs[a.RunID-1].AutomationID
	m.approvs = append(m.approvs, a)
	return a.ID, nil
}
func (m *memStore) AutomationApprovalByID(_ context.Context, id int64) (store.AutomationApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.approvs) {
		return store.AutomationApproval{}, store.ErrNotFound
	}
	return m.approvs[id-1], nil
}
func (m *memStore) PendingAutomationApprovals(context.Context) ([]store.AutomationApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AutomationApproval
	for _, a := range m.approvs {
		if a.State == store.ApprovalPending {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memStore) DecideAutomationApproval(_ context.Context, id int64, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.approvs) || m.approvs[id-1].State != store.ApprovalPending {
		return store.ErrNotFound
	}
	m.approvs[id-1].State = state
	return nil
}
func (m *memStore) PruneAutomationApprovals(context.Context, time.Time) error { return nil }
func (m *memStore) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.settings[k]
	return v, ok, nil
}
func (m *memStore) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[k] = v
	return nil
}
func (m *memStore) Audit(_ context.Context, e store.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, e)
	return nil
}

func (m *memStore) states() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.runs {
		var rec RunRecord
		_ = json.Unmarshal(r.Record, &rec)
		out = append(out, r.State+":"+rec.Reason)
	}
	return out
}

type engineRig struct {
	t   *testing.T
	st  *memStore
	reg *actions.Registry
	e   *Engine
	now time.Time
	// clockMu guards now where a background source (fsnotify, logind)
	// reads the clock while the test moves it: see advance.
	clockMu sync.Mutex
	ran     []string
	mu      sync.Mutex
	notes   []string
}

func newEngineRig(t *testing.T) *engineRig {
	g := &engineRig{t: t, st: &memStore{settings: map[string]json.RawMessage{}}, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)}
	g.reg = actions.NewRegistry(g.st)
	add := func(kind, name string, risk actions.Risk, auth actions.Authority, fail bool) {
		g.reg.Register(actions.Action{Kind: kind, Name: name, Risk: risk, Authority: auth,
			Validate: func(actions.Request) error { return nil },
			Plan:     func(context.Context, actions.Request) (actions.Plan, error) { return actions.Plan{}, nil },
			Run: func(ctx context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
				// Triggered runs are unattended; only approved steps run with
				// the user there, and say so.
				line := kind + "/" + name + " " + r.Target
				if !unattended.Is(ctx) {
					line += " (attended)"
				}
				g.mu.Lock()
				g.ran = append(g.ran, line)
				g.mu.Unlock()
				if fail {
					return actions.Result{}, errors.New("it failed")
				}
				return actions.Result{Done: 1}, nil
			}})
	}
	add("power", "profile", actions.Safe, actions.AuthorityNone, false)
	add("unit", "restart", actions.Reversible, actions.AuthorityNone, false)
	add("container", "restart", actions.Reversible, actions.AuthorityPolkit, false)
	add("image", "remove", actions.Destructive, actions.AuthorityNone, false)
	add("x", "flaky", actions.Safe, actions.AuthorityNone, true)
	add("x", "flakyrev", actions.Reversible, actions.AuthorityNone, true)
	g.e = NewEngine(Config{Store: g.st, Registry: g.reg, Clock: func() time.Time { g.clockMu.Lock(); defer g.clockMu.Unlock(); return g.now },
		Sources: modes.Sources{
			Next:        func(context.Context, string) (time.Time, error) { return g.now.Add(time.Minute), nil },
			Connections: func(context.Context) (map[string]bool, error) { return g.conns(), nil },
			Battery:     func() (float64, bool, bool) { return 50, g.unplugged(), true },
			Displays:    func() (int, error) { return g.displays(), nil },
		},
		Notify: func(_ context.Context, s, _ string) { g.mu.Lock(); g.notes = append(g.notes, s); g.mu.Unlock() },
		Sleep:  func(context.Context, time.Duration) error { return nil }})
	return g
}

var (
	connsUp   = map[string]bool{}
	unplugged bool
	displaysN = 1
	stateMu   sync.Mutex
)

func (g *engineRig) conns() map[string]bool { stateMu.Lock(); defer stateMu.Unlock(); return connsUp }
func (g *engineRig) unplugged() bool        { stateMu.Lock(); defer stateMu.Unlock(); return unplugged }
func (g *engineRig) displays() int          { stateMu.Lock(); defer stateMu.Unlock(); return displaysN }

func (g *engineRig) add(name, raw string) int64 {
	g.t.Helper()
	s, err := spec.Parse([]byte(raw))
	if err != nil {
		g.t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	g.st.autos = append(g.st.autos, store.Automation{ID: int64(len(g.st.autos) + 1), Name: name, Enabled: true, Spec: b, Origin: "ui"})
	if err := g.e.Load(context.Background()); err != nil {
		g.t.Fatal(err)
	}
	return int64(len(g.st.autos))
}

func (g *engineRig) observe(metric string, v map[string]float64) {
	g.e.Observe(context.Background(), g.now, metric, v)
	g.e.Wait()
}

func (g *engineRig) ranList() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.ran, ",")
}

func TestMetricTriggerAndCooldown(t *testing.T) {
	g := newEngineRig(t)
	g.add("heat", `{"trigger":{"kind":"metric","metric":"cpu.temp","op":">","threshold":90,"for_s":60},
		"then":[{"step":"action","action":"power/profile","target":"power-saver"}],"options":{"cooldown_s":600,"max_per_hour":6}}`)
	g.observe("cpu.temp", map[string]float64{"": 95})
	g.now = g.now.Add(30 * time.Second)
	g.observe("cpu.temp", map[string]float64{"": 95})
	if g.ranList() != "" {
		t.Fatal("fired before its duration")
	}
	g.now = g.now.Add(31 * time.Second)
	g.observe("cpu.temp", map[string]float64{"": 96})
	if g.ranList() != "power/profile power-saver" {
		t.Fatalf("%q", g.ranList())
	}
	// Back to normal, then hot again within the cooldown: no second run.
	g.now = g.now.Add(time.Minute)
	g.observe("cpu.temp", map[string]float64{"": 50})
	g.now = g.now.Add(2 * time.Minute)
	g.observe("cpu.temp", map[string]float64{"": 95})
	g.now = g.now.Add(time.Minute)
	g.observe("cpu.temp", map[string]float64{"": 95})
	if strings.Count(g.ranList(), "power/profile") != 1 {
		t.Fatalf("fired again within its cooldown: %q", g.ranList())
	}
	if s := g.st.states(); len(s) != 2 || s[0] != "done:" || s[1] != "skipped:cooldown" {
		t.Fatalf("%v", s)
	}
}

func TestEventTriggersMatchTargets(t *testing.T) {
	g := newEngineRig(t)
	g.add("unit", `{"trigger":{"kind":"unit_failed","scope":"user","target":"sync.service"},
		"then":[{"step":"action","action":"unit/restart","target":"user/sync.service"}]}`)
	g.observe("unit.failed", map[string]float64{"system/sync.service": 1, "user/other.service": 1})
	if g.ranList() != "" {
		t.Fatalf("%q", g.ranList())
	}
	g.observe("unit.failed", map[string]float64{"user/sync.service": 1})
	if g.ranList() != "unit/restart user/sync.service" {
		t.Fatalf("%q", g.ranList())
	}
}

func TestSelfTriggerIgnored(t *testing.T) {
	g := newEngineRig(t)
	g.add("heal", `{"trigger":{"kind":"unit_failed"},"then":[{"step":"action","action":"unit/restart","target":"user/a.service"}],
		"options":{"cooldown_s":60,"max_per_hour":60}}`)
	g.observe("unit.failed", map[string]float64{"user/a.service": 1})
	// The restart's own failure, past the cooldown's start but within it.
	g.observe("unit.failed", map[string]float64{})
	g.now = g.now.Add(70 * time.Second)
	g.observe("unit.failed", map[string]float64{"user/a.service": 1})
	if n := strings.Count(g.ranList(), "restart"); n != 2 {
		t.Fatalf("after the cooldown it may run again: %q", g.ranList())
	}
	// Within the tag's window, an event on the instance it touched is not a run at all.
	before := len(g.st.states())
	g.observe("unit.failed", map[string]float64{})
	g.now = g.now.Add(10 * time.Second)
	g.observe("unit.failed", map[string]float64{"user/a.service": 1})
	if len(g.st.states()) != before {
		t.Fatalf("its own action triggered it: %v", g.st.states())
	}
}

func TestApprovalNeededStopsAndRunsNothing(t *testing.T) {
	g := newEngineRig(t)
	for _, act := range []string{"container/restart", "image/remove"} {
		g.add(act, `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"power/profile","target":"balanced"},
			{"step":"action","action":"`+act+`","target":"web"},{"step":"action","action":"power/profile","target":"power-saver"}]}`)
	}
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	if r := g.ranList(); strings.Contains(r, "container") || strings.Contains(r, "image") || strings.Contains(r, "power-saver") {
		t.Fatalf("ran past a step needing approval: %q", r)
	}
	if s := g.st.states(); len(s) != 2 || !strings.HasPrefix(s[0], "waiting") || !strings.HasPrefix(s[1], "waiting") {
		t.Fatalf("%v", s)
	}
}

func TestKillSwitchAndGlobalLimit(t *testing.T) {
	g := newEngineRig(t)
	g.add("a", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"power/profile","target":"x"}]}`)
	if err := g.e.SetPaused(context.Background(), true, "test"); err != nil {
		t.Fatal(err)
	}
	g.observe("usb.new_device", map[string]float64{"1": 1})
	if g.ranList() != "" || g.st.states()[0] != "skipped:all automations are paused" {
		t.Fatalf("%q %v", g.ranList(), g.st.states())
	}
	_ = g.e.SetPaused(context.Background(), false, "test")
	g.e.cfg.GlobalLimit = 2
	g.add("burst", `{"trigger":{"kind":"new_listener"},"then":[{"step":"action","action":"power/profile","target":"1"},
		{"step":"action","action":"power/profile","target":"2"},{"step":"action","action":"power/profile","target":"3"}]}`)
	g.observe("net.new_listener", map[string]float64{"0.0.0.0:80": 1})
	if strings.Count(g.ranList(), "power/profile") != 2 || !g.e.Paused(context.Background()) || len(g.notes) != 1 {
		t.Fatalf("%q paused=%v %v", g.ranList(), g.e.Paused(context.Background()), g.notes)
	}
	if s := g.st.states(); !strings.HasPrefix(s[len(s)-1], "limited") {
		t.Fatalf("%v", s)
	}
}

func TestConditionsAndQuietHours(t *testing.T) {
	g := newEngineRig(t)
	g.add("night", `{"trigger":{"kind":"usb_device"},"if":{"match":"all","conditions":[{"kind":"time","from":"22:00","to":"06:00"}]},
		"then":[{"step":"action","action":"power/profile","target":"x"}]}`)
	g.add("quiet", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"power/profile","target":"y"}],
		"options":{"cooldown_s":300,"max_per_hour":6,"quiet_from":"11:00","quiet_to":"13:00"}}`)
	g.add("any", `{"trigger":{"kind":"usb_device"},"if":{"match":"any","conditions":[{"kind":"time","from":"22:00","to":"06:00"},{"kind":"power","on":"ac"}]},
		"then":[{"step":"action","action":"power/profile","target":"z"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	if g.ranList() != "power/profile z" {
		t.Fatalf("%q", g.ranList())
	}
	got := strings.Join(g.st.states(), ",")
	if !strings.Contains(got, "skipped:conditions did not hold") || !strings.Contains(got, "skipped:quiet hours") {
		t.Fatalf("%s", got)
	}
}

func TestContinueOnFailure(t *testing.T) {
	g := newEngineRig(t)
	g.add("safe", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"x/flaky","target":"1","continue":true},{"step":"action","action":"power/profile","target":"after-safe"}]}`)
	g.add("rev", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"x/flakyrev","target":"1","continue":true},{"step":"action","action":"power/profile","target":"after-rev"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	if r := g.ranList(); !strings.Contains(r, "after-safe") || strings.Contains(r, "after-rev") {
		t.Fatalf("only a safe step may continue: %q", r)
	}
}

func TestPolledTriggers(t *testing.T) {
	g := newEngineRig(t)
	uuid := "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"
	g.add("sched", `{"trigger":{"kind":"schedule","calendar":"*-*-* 12:01"},"then":[{"step":"action","action":"power/profile","target":"sched"}]}`)
	g.add("vpn", `{"trigger":{"kind":"vpn","uuid":"`+uuid+`","state":"up"},"then":[{"step":"action","action":"power/profile","target":"vpn"}]}`)
	g.add("unplug", `{"trigger":{"kind":"power","event":"unplugged"},"then":[{"step":"action","action":"power/profile","target":"unplug"}]}`)
	g.add("screen", `{"trigger":{"kind":"display","event":"connected"},"then":[{"step":"action","action":"power/profile","target":"screen"}]}`)
	stateMu.Lock()
	connsUp, unplugged, displaysN = map[string]bool{uuid: true}, true, 1 // as found: never fires
	stateMu.Unlock()
	tick := func() { g.e.Tick(context.Background()); g.e.Wait() }
	tick()
	if g.ranList() != "" {
		t.Fatalf("fired on what it first found: %q", g.ranList())
	}
	stateMu.Lock()
	connsUp, unplugged, displaysN = map[string]bool{}, false, 2
	stateMu.Unlock()
	g.now = g.now.Add(2 * time.Minute) // the schedule came
	tick()
	stateMu.Lock()
	connsUp, unplugged = map[string]bool{uuid: true}, true
	stateMu.Unlock()
	tick()
	for _, want := range []string{"sched", "vpn", "unplug", "screen"} {
		if !strings.Contains(g.ranList(), "power/profile "+want) {
			t.Errorf("%s did not fire: %q", want, g.ranList())
		}
	}
}

func TestLoginAndInvalidSpecLeftOut(t *testing.T) {
	g := newEngineRig(t)
	g.add("login", `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"power/profile","target":"login"}]}`)
	g.st.autos = append(g.st.autos, store.Automation{ID: 9, Name: "bad", Enabled: true, Spec: json.RawMessage(`{"trigger":{"kind":"shell"}}`)})
	if err := g.e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.e.Login(context.Background())
	g.e.Wait()
	if g.ranList() != "power/profile login" {
		t.Fatalf("%q", g.ranList())
	}
}
