package unitctl

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/dropin"
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

func opOf(n string) op {
	for _, o := range ops {
		if o.name == n {
			return o
		}
	}
	panic("no op " + n)
}

func TestPlan(t *testing.T) {
	c := &ctl{cfg: Config{Self: "user/hearthd.service"}}
	p, _ := c.plan(opOf("stop"), actions.Request{Target: "system/cups.service"})
	if p.Risk != "" || p.Confirm != "" || len(p.Items) != 1 {
		t.Errorf("plain stop %+v", p)
	}
	for _, o := range []string{"stop", "restart", "disable"} {
		p, _ := c.plan(opOf(o), actions.Request{Target: "system/NetworkManager.service"})
		if p.Risk != actions.Destructive || p.Confirm != "NetworkManager.service" {
			t.Errorf("critical %s %+v", o, p)
		}
	}
	for _, o := range []string{"start", "enable"} {
		if p, _ := c.plan(opOf(o), actions.Request{Target: "system/NetworkManager.service"}); p.Risk != "" {
			t.Errorf("critical %s escalated", o)
		}
	}
	if p, _ := c.plan(opOf("stop"), actions.Request{Target: "user/hearthd.service"}); len(p.Items) != 0 || p.Blocked[0].Reason != ReasonSelf {
		t.Errorf("self stop %+v", p)
	}
	if p, _ := c.plan(opOf("start"), actions.Request{Target: "user/hearthd.service"}); len(p.Items) != 1 {
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
func (b *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	return b.call(method, out, args, false)
}
func (b *fakeBus) CallInteractive(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	return b.call(method, out, args, true)
}

// call plays systemd; inter records the calls made with interactive
// authorization.
func (b *fakeBus) call(method string, out []any, args []any, interactive bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := strings.TrimPrefix(method, manager+".")
	b.calls = append(b.calls, m)
	if interactive {
		b.inter = append(b.inter, m)
	}
	if m == "Subscribe" {
		return nil
	}
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
			p, err := c.plan(o, actions.Request{Target: target})
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.run(context.Background(), o, actions.Request{Target: target}, p)
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
	if r, err := c.run(context.Background(), ops[0], actions.Request{Target: "user/syncnotes.service"}, actions.Plan{Items: []actions.Item{{ID: "user/syncnotes.service"}}}); err != nil || r.Done != 1 {
		t.Fatalf("user start %+v %v", r, err)
	}
}

func TestRegister(t *testing.T) {
	reg := actions.NewRegistry(nil)
	Register(reg, Config{})
}

type fakeBroker struct {
	method string
	params any
	err    error
}

func (f *fakeBroker) Call(_ context.Context, method string, params, _ any) error {
	f.method, f.params = method, params
	return f.err
}

func TestMaskAndUnmask(t *testing.T) {
	b := newBus()
	if res := run(t, b, "mask", "system/cups.service"); res.Done != 1 {
		t.Fatalf("%+v", res)
	}
	if res := run(t, b, "unmask", "system/cups.service"); res.Done != 1 {
		t.Fatalf("%+v", res)
	}
	if want := []string{"MaskUnitFiles", "Reload", "UnmaskUnitFiles", "Reload"}; !slices.Equal(b.inter, want) {
		t.Fatalf("%v", b.inter)
	}
	c := &ctl{cfg: Config{Self: "user/hearthd.service"}}
	if p, _ := c.plan(opOf("mask"), actions.Request{Target: "system/hearth-broker.socket"}); len(p.Items) != 0 || p.Blocked[0].Reason != ReasonSelf {
		t.Fatalf("%+v", p)
	}
	if p, _ := c.plan(opOf("mask"), actions.Request{Target: "system/NetworkManager.service"}); p.Risk != actions.Destructive || p.Confirm == "" {
		t.Fatalf("%+v", p)
	}
}

func TestOverride(t *testing.T) {
	dir := t.TempDir()
	b := newBus()
	fb := &fakeBroker{}
	c := &ctl{cfg: Config{UserDir: dir, Broker: fb, User: func(context.Context) (Bus, error) { return b, nil }}}
	d := map[string]string{"MemoryMax": "1G"}
	r := actions.Request{Target: "user/a.service", Params: d}
	p, err := c.plan(opOf("override"), r)
	if err != nil || len(p.Items) != 1 || !strings.Contains(p.Items[0].Detail, "MemoryMax=1G") {
		t.Fatalf("%+v %v", p, err)
	}
	if res, err := c.run(context.Background(), opOf("override"), r, p); err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	// The user's own manager: no interactive authorization (S15d).
	if got, _ := dropin.Read(dir, "a.service"); got["MemoryMax"] != "1G" || !slices.Equal(b.calls, []string{"Reload"}) || len(b.inter) != 0 {
		t.Fatalf("%v %v %v", got, b.calls, b.inter)
	}
	if fb.method != "" {
		t.Fatal("a user unit went to the broker")
	}

	r.Target = "system/a.service"
	if res, err := c.run(context.Background(), opOf("override"), r, p); err != nil || res.Done != 1 || fb.method != "unit.override" {
		t.Fatalf("%+v %v %s", res, err, fb.method)
	}
	if got := fb.params.(dropin.Params); got.Unit != "a.service" || got.Directives["MemoryMax"] != "1G" {
		t.Fatalf("%+v", got)
	}
	fb.err = fmt.Errorf("wrapped: %w", broker.ErrUnauthorized)
	if res, _ := c.run(context.Background(), opOf("override"), r, p); res.Failed[0].Reason != ReasonUnauthorized {
		t.Fatalf("%+v", res)
	}
	for _, target := range []string{"system/hearth-broker.service", "user/hearthd.service"} {
		if p, _ := c.plan(opOf("override"), actions.Request{Target: target}); len(p.Items) != 0 || p.Blocked[0].Reason != ReasonSelf {
			t.Fatalf("%s: %+v", target, p)
		}
	}
	if p, _ := c.plan(opOf("override"), actions.Request{Target: "system/dbus.service"}); p.Risk != actions.Destructive {
		t.Fatalf("%+v", p)
	}
}

func TestOverrideValidation(t *testing.T) {
	reg := actions.NewRegistry(nil)
	Register(reg, Config{})
	a, ok := reg.Lookup("unit/override")
	if !ok {
		t.Fatal("not registered")
	}
	for _, r := range []actions.Request{
		{Target: "system/a.service", Params: map[string]string{"ExecStart": "/bin/sh"}},
		{Target: "system/a.service", Params: map[string]string{"MemoryMax": "1G\nUser=root"}},
		{Target: "system/a.socket", Params: map[string]string{"Nice": "1"}},
		{Target: "system/a@.service", Params: map[string]string{"Nice": "1"}},
	} {
		if a.Validate(r) == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	m, _ := reg.Lookup("unit/mask")
	if m.Validate(actions.Request{Target: "system/a.service", Params: map[string]string{"x": "y"}}) == nil {
		t.Error("mask took params")
	}
}

func TestCap(t *testing.T) {
	b := newBus()
	c := &ctl{cfg: Config{User: func(context.Context) (Bus, error) { return b, nil }}}
	a := c.capAction()
	for _, r := range []actions.Request{
		{Target: "system/cups.service", Params: map[string]string{"MemoryMax": "1G"}},
		{Target: "user/a.socket", Params: map[string]string{"MemoryMax": "1G"}},
		{Target: "user/a.service", Params: map[string]string{"MemoryMax": "1M"}},
		{Target: "user/a.service", Params: map[string]string{"MemoryMax": "50%"}},
		{Target: "user/a.service", Params: map[string]string{"CPUQuota": "150"}},
		{Target: "user/a.service", Params: map[string]string{"CPUQuota": "0%"}},
		{Target: "user/a.service", Params: map[string]string{"TasksMax": "1"}},
		{Target: "user/a.service", Params: map[string]string{"ExecStart": "/bin/sh"}},
	} {
		if a.Validate(r) == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	r := actions.Request{Target: "user/app-hearth-x@1.service", Params: map[string]string{"MemoryMax": "1G", "CPUQuota": "150%"}}
	if err := a.Validate(r); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if res, err := a.Run(context.Background(), r, p); err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Equal(b.calls, []string{"SetUnitProperties"}) || len(b.inter) != 0 {
		t.Fatalf("%v %v", b.calls, b.inter)
	}
	for _, target := range []string{"user/hearthd.service", "user/dbus.service"} {
		if p, _ := a.Plan(context.Background(), actions.Request{Target: target}); len(p.Items) != 0 || p.Blocked[0].Reason != ReasonCritical {
			t.Errorf("%s: %+v", target, p)
		}
	}
}

func TestCapProps(t *testing.T) {
	props, err := capProps(map[string]string{"MemoryMax": "1G", "CPUQuota": "150%"})
	if err != nil || props[0].Value.Value() != uint64(1<<30) || props[1].Value.Value() != uint64(1_500_000) {
		t.Fatalf("%+v %v", props, err)
	}
	props, err = capProps(nil)
	if err != nil || props[0].Value.Value() != uint64(math.MaxUint64) || props[1].Value.Value() != uint64(math.MaxUint64) {
		t.Fatalf("clearing: %+v %v", props, err)
	}
}

func FuzzCapProps(f *testing.F) {
	f.Add("MemoryMax", "1G")
	f.Add("CPUQuota", "150%")
	f.Add("ExecStart", "/bin/sh")
	f.Fuzz(func(t *testing.T, k, v string) {
		if _, err := capProps(map[string]string{k: v}); err == nil && k != "MemoryMax" && k != "CPUQuota" {
			t.Fatalf("accepted %q", k)
		}
	})
}
