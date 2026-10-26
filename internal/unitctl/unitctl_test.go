package unitctl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

func TestTarget(t *testing.T) {
	for s, ok := range map[string]bool{
		"system/cups.service": true, "user/syncnotes.service": true, "system/getty@tty1.service": true,
		"system/apt-daily.timer": true, `system/app-x\x2dy.service`: true,
		"cups.service": false, "root/cups.service": false, "system/-x.service": false, "system/cups": false,
		"system/getty@.service": false, "system/a b.service": false, "system/a\n.service": false, "user/": false,
	} {
		if _, _, err := Target(s); (err == nil) != ok {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestPlan(t *testing.T) {
	c := &ctl{cfg: Config{Self: "user/hearthd.service"}}
	opOf := func(n string) op {
		for _, o := range ops {
			if o.name == n {
				return o
			}
		}
		t.Fatalf("no op %s", n)
		return op{}
	}
	p, _ := c.plan(opOf("stop"), "system/cups.service")
	if p.Risk != "" || p.Confirm != "" || len(p.Items) != 1 {
		t.Errorf("plain stop %+v", p)
	}
	for _, o := range []string{"stop", "restart", "disable"} {
		p, _ := c.plan(opOf(o), "system/NetworkManager.service")
		if p.Risk != actions.Destructive || p.Confirm != "NetworkManager.service" {
			t.Errorf("critical %s %+v", o, p)
		}
	}
	for _, o := range []string{"start", "enable"} {
		if p, _ := c.plan(opOf(o), "system/NetworkManager.service"); p.Risk != "" {
			t.Errorf("critical %s escalated", o)
		}
	}
	if p, _ := c.plan(opOf("stop"), "user/hearthd.service"); len(p.Items) != 0 || p.Blocked[0].Reason != ReasonSelf {
		t.Errorf("self stop %+v", p)
	}
	if p, _ := c.plan(opOf("start"), "user/hearthd.service"); len(p.Items) != 1 {
		t.Errorf("self start blocked %+v", p)
	}
}

// fakeBus plays a systemd manager.
type fakeBus struct {
	mu      sync.Mutex
	calls   []string
	inter   []string // methods called with interactive authorization
	result  string   // JobRemoved result; "" sends none
	err     error    // returned by the interactive call
	carries bool
	sigs    chan *dbus.Signal
}

func newBus() *fakeBus {
	return &fakeBus{result: "done", carries: true, sigs: make(chan *dbus.Signal, 4)}
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
func (b *fakeBus) Close() error { return nil }
func (b *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, _ []any, _ ...any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, strings.TrimPrefix(method, manager+"."))
	return nil
}
func (b *fakeBus) CallInteractive(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := strings.TrimPrefix(method, manager+".")
	b.calls = append(b.calls, m)
	b.inter = append(b.inter, m)
	if b.err != nil && m != "Reload" {
		return b.err
	}
	switch m {
	case "StartUnit", "StopUnit", "RestartUnit":
		if args[1] != "replace" {
			return errors.New("bad mode")
		}
		*out[0].(*dbus.ObjectPath) = "/job/9"
		if b.result != "" {
			b.sigs <- &dbus.Signal{Body: []any{uint32(9), dbus.ObjectPath("/job/9"), args[0], b.result}}
		}
	case "EnableUnitFiles":
		*out[0].(*bool) = b.carries
	}
	return nil
}

func run(t *testing.T, b *fakeBus, opName, target string) actions.Result {
	t.Helper()
	c := &ctl{cfg: Config{System: func(context.Context) (Bus, error) { return b, nil },
		User: func(context.Context) (Bus, error) { return nil, errors.New("wrong bus") }, Self: "user/hearthd.service", JobWait: 50 * time.Millisecond}}
	for _, o := range ops {
		if o.name == opName {
			p, err := c.plan(o, target)
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.run(context.Background(), o, target, p)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
	}
	t.Fatalf("no op %s", opName)
	return actions.Result{}
}

func TestRun(t *testing.T) {
	b := newBus()
	if r := run(t, b, "restart", "system/cups.service"); r.Done != 1 || strings.Join(b.calls, ",") != "Subscribe,RestartUnit" || strings.Join(b.inter, ",") != "RestartUnit" {
		t.Fatalf("restart %+v %v", r, b.calls)
	}
	b = newBus()
	if r := run(t, b, "enable", "system/cups.service"); r.Done != 1 || strings.Join(b.inter, ",") != "EnableUnitFiles,Reload" {
		t.Fatalf("enable %+v %v", r, b.inter)
	}
	b = newBus()
	if r := run(t, b, "disable", "system/cups.service"); r.Done != 1 || strings.Join(b.inter, ",") != "DisableUnitFiles,Reload" {
		t.Fatalf("disable %+v %v", r, b.inter)
	}
	// Still running after JobWait: reported done, the list follows it.
	b = newBus()
	b.result = ""
	if r := run(t, b, "start", "system/slow.service"); r.Done != 1 {
		t.Fatalf("slow start %+v", r)
	}
	for _, c := range []struct {
		setup  func(b *fakeBus)
		op     string
		reason string
		detail string
	}{
		{func(b *fakeBus) { b.result = "failed" }, "start", ReasonJobFailed, "failed"},
		{func(b *fakeBus) { b.carries = false }, "enable", ReasonNoInstall, ""},
		{func(b *fakeBus) { b.err = dbus.Error{Name: "org.freedesktop.PolicyKit1.Error.NotAuthorized"} }, "stop", ReasonUnauthorized, ""},
		{func(b *fakeBus) {
			b.err = dbus.Error{Name: "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired"}
		}, "stop", ReasonUnauthorized, ""},
		{func(b *fakeBus) { b.err = dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"} }, "start", ReasonNotFound, ""},
		{func(b *fakeBus) { b.err = dbus.Error{Name: "org.freedesktop.systemd1.UnitMasked"} }, "start", ReasonMasked, ""},
		{func(b *fakeBus) { b.err = errors.New("boom") }, "start", ReasonError, "boom"},
	} {
		b := newBus()
		c.setup(b)
		r := run(t, b, c.op, "system/x.service")
		if r.Done != 0 || len(r.Failed) != 1 || r.Failed[0].Reason != c.reason || (c.detail != "" && r.Failed[0].Detail != c.detail) {
			t.Errorf("%s %s: %+v", c.op, c.reason, r)
		}
	}
	// User units go to the user bus.
	c := &ctl{cfg: Config{System: func(context.Context) (Bus, error) { return nil, errors.New("wrong bus") },
		User: func(context.Context) (Bus, error) { return newBus(), nil }, JobWait: time.Second}}
	if r, err := c.run(context.Background(), ops[0], "user/syncnotes.service", actions.Plan{Items: []actions.Item{{ID: "user/syncnotes.service"}}}); err != nil || r.Done != 1 {
		t.Fatalf("user start %+v %v", r, err)
	}
}

func TestRegister(t *testing.T) {
	reg := actions.NewRegistry(nil)
	Register(reg, Config{})
}
