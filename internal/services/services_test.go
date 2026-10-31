package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestMerge(t *testing.T) {
	loaded := []listed{
		{Name: "cups.service", Description: "CUPS", Load: "loaded", Active: "active", Sub: "running"},
		{Name: "foo.service", Load: "masked", Active: "inactive", Sub: "dead"},
		{Name: "bar.service", Load: "loaded", Active: "activating", Sub: "start", JobType: "start"},
		{Name: "acct.service", Load: "not-found", Active: "inactive", Sub: "dead"},
	}
	files := map[string]string{"cups.service": "enabled", "ssh.service": "disabled", "getty@.service": "enabled"}
	got := merge(ScopeSystem, loaded, files)
	names := []string{}
	for _, u := range got {
		names = append(names, u.Name)
	}
	if strings.Join(names, ",") != "bar.service,cups.service,foo.service,ssh.service" {
		t.Fatalf("names %v", names)
	}
	byName := map[string]Unit{}
	for _, u := range got {
		byName[u.Name] = u
	}
	if u := byName["cups.service"]; u.Enabled != "enabled" || u.Sub != "running" || u.Scope != ScopeSystem || u.Description != "CUPS" {
		t.Errorf("cups %+v", u)
	}
	if u := byName["ssh.service"]; u.Active != "inactive" || u.Enabled != "disabled" || u.Load != "" {
		t.Errorf("not loaded %+v", u)
	}
	if byName["foo.service"].Enabled != "masked" || byName["bar.service"].Job != "start" {
		t.Errorf("masked/job %+v %+v", byName["foo.service"], byName["bar.service"])
	}
}

func TestCritical(t *testing.T) {
	for _, c := range []struct {
		scope, name string
		want        bool
	}{
		{ScopeSystem, "dbus.service", true}, {ScopeSystem, "dbus.socket", true}, {ScopeSystem, "systemd-journald.service", true},
		{ScopeSystem, "NetworkManager.service", true}, {ScopeSystem, "polkit.service", true}, {ScopeSystem, "gdm.service", true},
		{ScopeSystem, "display-manager.service", true}, {ScopeSystem, "user@1000.service", true}, {ScopeSystem, "-.mount", true},
		{ScopeSystem, "getty@tty1.service", true}, {ScopeSystem, "multi-user.target", true}, {ScopeSystem, "system.slice", true},
		{ScopeUser, "hearthd.service", true}, {ScopeUser, "org.gnome.Shell@wayland.service", true}, {ScopeUser, "pipewire.service", true},
		{ScopeUser, "dbus.service", true},
		{ScopeSystem, "cups.service", false}, {ScopeSystem, "docker.service", false}, {ScopeSystem, "ssh.service", false},
		{ScopeSystem, "home-me-data.mount", false}, {ScopeUser, "syncnotes.service", false}, {ScopeUser, "systemd-tmpfiles-clean.timer", false},
	} {
		if got := Critical(c.scope, c.name); got != c.want {
			t.Errorf("%s %s: %v", c.scope, c.name, got)
		}
	}
}

func cgTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, v := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStats(t *testing.T) {
	root := t.TempDir()
	const u = "user.slice/user-1000.slice/user@1000.service/"
	cgTree(t, root, map[string]string{
		"system.slice/cups.service/memory.current":                          "1048576\n",
		"system.slice/cups.service/cpu.stat":                                "usage_usec 1000000\nuser_usec 5\n",
		"system.slice/system-getty.slice/getty@tty1.service/memory.current": "4096\n",
		"system.slice/idle.service/memory.current":                          "0\n",
		"user.slice/user-1000.slice/session-2.scope/memory.current":         "8192\n",
		u + "memory.current":                                                "999\n",
		u + "app.slice/syncnotes.service/memory.current":                    "2048\n",
		u + "app.slice/syncnotes.service/cpu.stat":                          "usage_usec 0\n",
		u + "session.slice/pipewire.service/memory.current":                 "512\n",
	})
	s := newStats(root, 1000)
	s.ncpu = 2
	t0 := time.Unix(100, 0)
	got := s.sample(t0)
	want := map[string]uint64{
		"system/cups.service": 1048576, "system/getty@tty1.service": 4096, "system/session-2.scope": 8192,
		"system/user@1000.service": 999, "user/syncnotes.service": 2048, "user/pipewire.service": 512,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k].Mem != v || got[k].CPU != 0 {
			t.Errorf("%s: %+v", k, got[k])
		}
	}
	// One more second of CPU over 2 s on 2 CPUs is 25 %.
	cgTree(t, root, map[string]string{"system.slice/cups.service/cpu.stat": "usage_usec 2000000\n"})
	if got := s.sample(t0.Add(2 * time.Second)); got["system/cups.service"].CPU != 25 {
		t.Fatalf("cpu %+v", got["system/cups.service"])
	}
}

// fakeBus plays a systemd manager.
type fakeBus struct {
	mu     sync.Mutex
	units  []listed
	files  []unitFile
	sigs   chan *dbus.Signal
	calls  map[string]int
	closed bool
}

func newFakeBus(units ...listed) *fakeBus {
	return &fakeBus{units: units, sigs: make(chan *dbus.Signal, 8), calls: map[string]int{}}
}

func (b *fakeBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, errors.New("unused")
}
func (b *fakeBus) Set(context.Context, string, dbus.ObjectPath, string, string, any) error {
	return errors.New("unused")
}
func (b *fakeBus) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	return b.sigs, nil
}
func (b *fakeBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
func (b *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, _ ...any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[strings.TrimPrefix(method, manager+".")]++
	switch method {
	case manager + ".ListUnits":
		*out[0].(*[]listed) = append([]listed{}, b.units...)
	case manager + ".ListUnitFiles":
		*out[0].(*[]unitFile) = append([]unitFile{}, b.files...)
	}
	return nil
}
func (b *fakeBus) set(units ...listed) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.units = units
}
func (b *fakeBus) count(m string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[m]
}

// fakeHub records publications and lets the test change subscribers.
type fakeHub struct {
	mu    sync.Mutex
	subs  int
	watch chan struct{}
	msgs  []any
}

func (h *fakeHub) Publish(_ string, data any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, data)
	return nil
}
func (h *fakeHub) Subscribers(string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs
}
func (h *fakeHub) Watch(string) <-chan struct{} { return h.watch }
func (h *fakeHub) setSubs(n int) {
	h.mu.Lock()
	h.subs = n
	h.mu.Unlock()
	h.watch <- struct{}{}
}
func (h *fakeHub) lists() []ListMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []ListMsg
	for _, m := range h.msgs {
		if l, ok := m.(ListMsg); ok {
			out = append(out, l)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatcher(t *testing.T) {
	sys := newFakeBus(listed{Name: "cups.service", Load: "loaded", Active: "active", Sub: "running"})
	sys.files = []unitFile{{Path: "/usr/lib/systemd/system/cups.service", State: "enabled"}}
	hub := &fakeHub{watch: make(chan struct{}, 1)}
	dialed := 0
	w := NewWatcher(Config{
		System:   func(context.Context) (Bus, error) { dialed++; return sys, nil },
		User:     func(context.Context) (Bus, error) { return nil, errors.New("no session bus") },
		Pub:      hub,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Debounce: 10 * time.Millisecond, StatsInterval: time.Hour, CgroupRoot: t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Nothing happens without subscribers.
	time.Sleep(30 * time.Millisecond)
	if dialed != 0 || len(hub.lists()) != 0 {
		t.Fatalf("worked while idle: dialed %d", dialed)
	}
	hub.setSubs(1)
	// The units at once, then again with their unit file states.
	waitFor(t, "lists", func() bool { return len(hub.lists()) == 2 })
	if l := hub.lists()[0]; len(l.Units) != 1 || l.Units[0].Enabled != "" || l.Errors["user"] == "" || sys.count("Subscribe") != 1 {
		t.Fatalf("first list %+v", l)
	}
	if l := hub.lists()[1]; l.Units[0].Enabled != "enabled" {
		t.Fatalf("second list %+v", l)
	}

	// A burst of signals is one reload; unit files only when they changed.
	sys.set(listed{Name: "cups.service", Load: "loaded", Active: "inactive", Sub: "dead"})
	for range 3 {
		sys.sigs <- &dbus.Signal{Name: "org.freedesktop.DBus.Properties.PropertiesChanged"}
	}
	waitFor(t, "changed list", func() bool { return len(hub.lists()) == 3 })
	if hub.lists()[2].Units[0].Active != "inactive" || sys.count("ListUnits") != 2 || sys.count("ListUnitFiles") != 1 {
		t.Fatalf("after change: %+v, ListUnits %d", hub.lists()[2], sys.count("ListUnits"))
	}
	sys.sigs <- &dbus.Signal{Name: manager + ".UnitFilesChanged"}
	waitFor(t, "unit files reload", func() bool { return sys.count("ListUnitFiles") == 2 })
	time.Sleep(30 * time.Millisecond)
	if n := len(hub.lists()); n != 3 {
		t.Fatalf("unchanged list republished: %d", n)
	}

	// A second client gets the list again.
	hub.setSubs(2)
	waitFor(t, "list for new client", func() bool { return len(hub.lists()) == 4 })

	// The last client leaves: the connection is closed.
	hub.setSubs(0)
	waitFor(t, "close", func() bool { sys.mu.Lock(); defer sys.mu.Unlock(); return sys.closed })
	cancel()
	<-done
}

// TestRealBus lists the units of this machine, if it has the buses.
func TestRealBus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for scope, dial := range map[string]func(context.Context) (*dbusx.Conn, error){
		ScopeSystem: dbusx.SystemBus, ScopeUser: dbusx.SessionBus,
	} {
		bus, err := dial(ctx)
		if err != nil {
			t.Logf("%s: no bus: %v", scope, err)
			continue
		}
		start := time.Now()
		loaded, err1 := listUnits(ctx, bus)
		files, err2 := listFiles(ctx, bus)
		_ = bus.Close()
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v %v", scope, err1, err2)
		}
		units := merge(scope, loaded, files)
		crit := 0
		for _, u := range units {
			if u.Critical {
				crit++
			}
		}
		t.Logf("%s: %d loaded, %d files, %d units (%d critical) in %v", scope, len(loaded), len(files), len(units), crit, time.Since(start))
	}
	st := newStats("/sys/fs/cgroup", os.Getuid())
	st.sample(time.Now())
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	got := st.sample(time.Now())
	t.Logf("stats: %d unit cgroups in %v", len(got), time.Since(start))
}

func TestParseJournal(t *testing.T) {
	out := `{"__CURSOR":"s=1;i=1","__REALTIME_TIMESTAMP":"1700000000123456","PRIORITY":"3","_PID":"42","SYSLOG_IDENTIFIER":"cupsd","MESSAGE":"failed <b>x</b>"}
{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1700000001000000","MESSAGE":[104,105,255]}
not json
{"__CURSOR":"s=1;i=3","MESSAGE":null}
{"__CURSOR":"s=1;i=4","MESS`
	got := parseJournal([]byte(out), true)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if e := got[0]; e.Time != 1700000000123 || e.Prio != 3 || e.PID != 42 || e.Ident != "cupsd" || e.Msg != "failed <b>x</b>" {
		t.Errorf("first %+v", e)
	}
	if e := got[1]; e.Msg != "hi�" || e.Prio != 6 {
		t.Errorf("binary %+v", e)
	}
	if got[2].Msg != "" {
		t.Errorf("null %+v", got[2])
	}
}

func TestReaderGroup(t *testing.T) {
	g := "root:x:0:\nadm:x:4:syslog\nsudo:x:27:me\nsystemd-journal:x:999:\n"
	if !readerGroup(g, []int{1000, 4}) || !readerGroup(g, []int{999}) || readerGroup(g, []int{27, 1000}) {
		t.Fatal("groups")
	}
}

func TestHandleJournal(t *testing.T) {
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if name != "journalctl-unit" {
			t.Fatalf("ran %s", name)
		}
		gotArgs = args
		return safeexec.Result{Stdout: []byte(`{"__CURSOR":"s=1;i=9","MESSAGE":"hello"}` + "\n")}, nil
	}
	h := HandleJournal(run, func() bool { return false })
	get := func(q string) (int, Journal) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/api/v1/services/journal?"+q, nil))
		var j Journal
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		return rec.Code, j
	}
	code, j := get("unit=system/cups.service")
	if code != 200 || len(j.Entries) != 1 || j.Cursor != "s=1;i=9" || !j.Limited ||
		strings.Join(gotArgs[len(gotArgs)-2:], " ") != "--lines=200 --unit=cups.service" {
		t.Fatalf("%d %+v %q", code, j, gotArgs)
	}
	code, j = get("unit=user/syncnotes.service&lines=50&after=s%3D1%3Bi%3D8")
	if code != 200 || j.Limited || strings.Join(gotArgs[len(gotArgs)-3:], " ") != "--lines=50 --user-unit=syncnotes.service --after-cursor=s=1;i=8" {
		t.Fatalf("%d %+v %q", code, j, gotArgs)
	}
	for _, q := range []string{"unit=cups.service", "unit=system/-x.service", "unit=root/cups.service", "unit=system/cups.service&lines=0", "unit=system/cups.service&lines=5000"} {
		if code, _ := get(q); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
}

func TestRealJournal(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleJournal(safeexec.Run, CanReadSystemJournal)(rec, httptest.NewRequest("GET", "/api/v1/services/journal?unit=user/pipewire.service&lines=5", nil))
	t.Logf("%d %.300s", rec.Code, rec.Body.String())
}

func TestUnitFilesLeavesOutRuntimeUnits(t *testing.T) {
	bus := &fakeBus{calls: map[string]int{}, files: []unitFile{
		{Path: "/usr/lib/systemd/system/cups.service", State: "enabled"},
		{Path: "/run/systemd/transient/run-1.service", State: "transient"},
		{Path: "/run/systemd/generator/home.mount", State: "generated"},
	}}
	got, err := UnitFiles(context.Background(), bus)
	if err != nil || len(got) != 1 || got["cups.service"] != "enabled" {
		t.Fatalf("%v %+v", err, got)
	}
}
