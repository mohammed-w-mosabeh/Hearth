package power

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// fakeBus is an in-memory UPower + power-profiles-daemon.
type fakeBus struct {
	mu      sync.Mutex
	devices map[dbus.ObjectPath]map[string]dbus.Variant
	ppd     map[string]map[string]dbus.Variant // by bus name
	sets    []string
	setErr  error
	watches []chan *dbus.Signal
}

func newFakeBus() *fakeBus {
	return &fakeBus{
		devices: map[dbus.ObjectPath]map[string]dbus.Variant{
			"/org/freedesktop/UPower/devices/battery_BAT1": battery(87.5, 2, 12.4),
			"/org/freedesktop/UPower/devices/line_power_ACAD": {
				"Type": dbus.MakeVariant(uint32(1)), "Online": dbus.MakeVariant(false),
			},
			"/org/freedesktop/UPower/devices/mouse_hid": { // peripheral battery: ignored
				"Type": dbus.MakeVariant(uint32(2)), "PowerSupply": dbus.MakeVariant(false), "IsPresent": dbus.MakeVariant(true),
				"Percentage": dbus.MakeVariant(40.0), "NativePath": dbus.MakeVariant("hid-mouse"),
			},
		},
		ppd: map[string]map[string]dbus.Variant{
			"org.freedesktop.UPower.PowerProfiles": profiles("balanced"),
		},
	}
}

func battery(pct float64, state uint32, rate float64) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"Type": dbus.MakeVariant(uint32(2)), "PowerSupply": dbus.MakeVariant(true), "IsPresent": dbus.MakeVariant(true),
		"NativePath": dbus.MakeVariant("BAT1"), "Model": dbus.MakeVariant("EXB-4200"), "Vendor": dbus.MakeVariant("Example"),
		"Percentage": dbus.MakeVariant(pct), "State": dbus.MakeVariant(state), "EnergyRate": dbus.MakeVariant(rate),
		"Energy": dbus.MakeVariant(28.1), "EnergyFull": dbus.MakeVariant(32.12), "EnergyFullDesign": dbus.MakeVariant(41.0),
		"Capacity": dbus.MakeVariant(0.0), "ChargeCycles": dbus.MakeVariant(int32(-1)),
		"TimeToEmpty": dbus.MakeVariant(int64(8160)), "TimeToFull": dbus.MakeVariant(int64(0)),
	}
}

func profiles(active string) map[string]dbus.Variant {
	prof := func(n string) map[string]dbus.Variant { return map[string]dbus.Variant{"Profile": dbus.MakeVariant(n)} }
	return map[string]dbus.Variant{
		"ActiveProfile":       dbus.MakeVariant(active),
		"Profiles":            dbus.MakeVariant([]map[string]dbus.Variant{prof("power-saver"), prof("balanced"), prof("performance")}),
		"PerformanceDegraded": dbus.MakeVariant(""),
	}
}

func (b *fakeBus) GetAll(_ context.Context, dest string, path dbus.ObjectPath, _ string) (map[string]dbus.Variant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if dest == upowerDest {
		if p, ok := b.devices[path]; ok {
			return p, nil
		}
		return nil, errors.New("no such object")
	}
	if p, ok := b.ppd[dest]; ok {
		return p, nil
	}
	return nil, errors.New("service unknown")
}

func (b *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, _ ...any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if method != upowerIface+".EnumerateDevices" {
		return errors.New("unexpected call " + method)
	}
	var paths []dbus.ObjectPath
	for p := range b.devices {
		paths = append(paths, p)
	}
	*(out[0].(*[]dbus.ObjectPath)) = paths
	return nil
}

func (b *fakeBus) Set(_ context.Context, dest string, _ dbus.ObjectPath, iface, prop string, value any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sets = append(b.sets, dest+" "+iface+"."+prop+"="+value.(string))
	if b.setErr != nil {
		return b.setErr
	}
	b.ppd[dest] = profiles(value.(string))
	return nil
}

func (b *fakeBus) Watch(ctx context.Context, _ ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	ch := make(chan *dbus.Signal, 8)
	b.mu.Lock()
	b.watches = append(b.watches, ch)
	b.mu.Unlock()
	go func() { <-ctx.Done() }()
	return ch, nil
}

func (b *fakeBus) emit(name string, path dbus.ObjectPath) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.watches {
		ch <- &dbus.Signal{Name: name, Path: path}
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func start(t *testing.T, bus *fakeBus) *Watcher {
	t.Helper()
	w := NewWatcher(func(context.Context) (dbusx.Bus, error) { return bus, nil }, discard())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return w.Snapshot().Profile != nil })
	return w
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInitialState(t *testing.T) {
	w := start(t, newFakeBus())
	st := w.Snapshot()
	if len(st.Batteries) != 1 {
		t.Fatalf("batteries %+v (peripheral must be excluded)", st.Batteries)
	}
	b := st.Batteries[0]
	if b.ID != "BAT1" || b.Pct != 87.5 || b.State != "discharging" || b.RateW != 12.4 || b.Cycles != 0 ||
		b.ToEmptySec != 8160 || b.Model != "EXB-4200" {
		t.Fatalf("battery %+v", b)
	}
	if want := 32.12 / 41.0 * 100; b.HealthPct < want-0.01 || b.HealthPct > want+0.01 {
		t.Fatalf("health %v, want %v (from energy when Capacity is 0)", b.HealthPct, want)
	}
	if st.OnAC == nil || *st.OnAC {
		t.Fatalf("on AC %v", st.OnAC)
	}
	if st.Profile.Active != "balanced" || strings.Join(st.Profile.Available, ",") != "power-saver,balanced,performance" {
		t.Fatalf("profile %+v", st.Profile)
	}
}

func TestSignalsUpdateState(t *testing.T) {
	bus := newFakeBus()
	w := start(t, bus)

	bus.mu.Lock()
	bus.devices["/org/freedesktop/UPower/devices/battery_BAT1"] = battery(88, 1, 20)
	bus.devices["/org/freedesktop/UPower/devices/line_power_ACAD"]["Online"] = dbus.MakeVariant(true)
	bus.mu.Unlock()
	bus.emit(propsIface+".PropertiesChanged", "/org/freedesktop/UPower/devices/battery_BAT1")
	bus.emit(propsIface+".PropertiesChanged", "/org/freedesktop/UPower/devices/line_power_ACAD")
	waitFor(t, func() bool {
		st := w.Snapshot()
		return len(st.Batteries) == 1 && st.Batteries[0].State == "charging" && st.OnAC != nil && *st.OnAC
	})

	bus.mu.Lock()
	delete(bus.devices, "/org/freedesktop/UPower/devices/battery_BAT1")
	bus.mu.Unlock()
	bus.emit(upowerIface+".DeviceRemoved", upowerPath)
	waitFor(t, func() bool { return len(w.Snapshot().Batteries) == 0 })

	bus.mu.Lock()
	bus.ppd["org.freedesktop.UPower.PowerProfiles"] = profiles("power-saver")
	bus.mu.Unlock()
	bus.emit(propsIface+".PropertiesChanged", ppdNames[0].path)
	waitFor(t, func() bool { return w.Snapshot().Profile.Active == "power-saver" })
}

func TestLegacyProfilesName(t *testing.T) {
	bus := newFakeBus()
	bus.ppd = map[string]map[string]dbus.Variant{"net.hadess.PowerProfiles": profiles("performance")}
	w := start(t, bus)
	if w.Snapshot().Profile.Active != "performance" {
		t.Fatal("did not fall back to net.hadess.PowerProfiles")
	}
	if err := w.SetProfile(context.Background(), "balanced"); err != nil {
		t.Fatal(err)
	}
	if got := bus.sets[0]; got != "net.hadess.PowerProfiles net.hadess.PowerProfiles.ActiveProfile=balanced" {
		t.Fatalf("set %q", got)
	}
}

func TestNoServices(t *testing.T) {
	bus := newFakeBus()
	bus.devices = map[dbus.ObjectPath]map[string]dbus.Variant{}
	bus.ppd = map[string]map[string]dbus.Variant{}
	w := NewWatcher(func(context.Context) (dbusx.Bus, error) { return bus, nil }, discard())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	st := w.Snapshot()
	if len(st.Batteries) != 0 || st.OnAC != nil || st.Profile != nil {
		t.Fatalf("%+v", st)
	}
	if err := w.SetProfile(context.Background(), "balanced"); !errors.Is(err, ErrNoProfiles) {
		t.Fatalf("got %v", err)
	}
	cancel()
	<-done
}

func TestReconnect(t *testing.T) {
	old := reconnectDelay
	reconnectDelay = 10 * time.Millisecond
	defer func() { reconnectDelay = old }()

	bus := newFakeBus()
	var mu sync.Mutex
	attempts := 0
	w := NewWatcher(func(context.Context) (dbusx.Bus, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts < 3 {
			return nil, errors.New("no bus yet")
		}
		return bus, nil
	}, discard())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	waitFor(t, func() bool { return w.Snapshot().Profile != nil })
}

func TestHandleSetProfile(t *testing.T) {
	bus := newFakeBus()
	w := start(t, bus)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	h := w.HandleSetProfile(db)

	post := func(body string) int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/api/v1/power/profile", strings.NewReader(body)))
		return rec.Code
	}
	if c := post(`{"profile":"performance"}`); c != 200 {
		t.Fatalf("valid: %d", c)
	}
	if w.Snapshot().Profile.Active != "performance" {
		t.Fatal("profile not applied")
	}
	if c := post(`{"profile":"turbo"}`); c != 400 {
		t.Fatalf("unknown profile: %d", c)
	}
	if len(bus.sets) != 1 {
		t.Fatalf("unknown profile reached D-Bus: %v", bus.sets)
	}
	for _, bad := range []string{``, `{}`, `{"profile":""}`, `{"profile":"balanced","x":1}`, `not json`} {
		if c := post(bad); c != 400 {
			t.Errorf("%q: %d", bad, c)
		}
	}
	bus.mu.Lock()
	bus.setErr = dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied", Body: []any{"denied by polkit"}}
	bus.mu.Unlock()
	if c := post(`{"profile":"power-saver"}`); c != 403 {
		t.Fatalf("polkit denial: %d", c)
	}

	// Every attempt that reached validation is audited, with the outcome.
	rows, err := db.QueryContext(context.Background(), `SELECT action, target, result FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var results []string
	for rows.Next() {
		var a, tg, res string
		if err := rows.Scan(&a, &tg, &res); err != nil {
			t.Fatal(err)
		}
		if a != "power.set_profile" || tg != "power:ppd:profile" {
			t.Fatalf("row %s %s", a, tg)
		}
		results = append(results, res)
	}
	if len(results) != 3 || results[0] != "ok" || !strings.Contains(results[1], "unknown profile") || !strings.Contains(results[2], "AccessDenied") {
		t.Fatalf("audit results %q", results)
	}
}

func TestRealSystemBus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dbusx.SystemBus(ctx)
	if err != nil {
		t.Skipf("no system bus: %v", err)
	}
	_ = conn.Close()
	w := NewWatcher(func(ctx context.Context) (dbusx.Bus, error) { return dbusx.SystemBus(ctx) }, discard())
	go w.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	st := w.Snapshot()
	for _, b := range st.Batteries {
		t.Logf("battery %s %.0f%% %s rate=%.1fW health=%.0f%% cycles=%d", b.ID, b.Pct, b.State, b.RateW, b.HealthPct, b.Cycles)
	}
	if st.OnAC != nil {
		t.Logf("on AC: %v", *st.OnAC)
	}
	if st.Profile != nil {
		t.Logf("profile %s of %v", st.Profile.Active, st.Profile.Available)
	}
}
