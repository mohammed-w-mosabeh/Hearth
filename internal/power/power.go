// Package power tracks batteries and AC through UPower and the power profile
// through power-profiles-daemon, both over the D-Bus system bus. State is
// updated from D-Bus signals, so it costs nothing while idle.
package power

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	upowerDest  = "org.freedesktop.UPower"
	upowerPath  = dbus.ObjectPath("/org/freedesktop/UPower")
	upowerIface = "org.freedesktop.UPower"
	deviceIface = "org.freedesktop.UPower.Device"
	propsIface  = "org.freedesktop.DBus.Properties"

	upowerTypeLinePower = 1
	upowerTypeBattery   = 2
)

// ppdNames are power-profiles-daemon's bus names, newest first (0.20 moved
// to the UPower namespace; older versions only own net.hadess).
var ppdNames = []struct {
	dest  string
	path  dbus.ObjectPath
	iface string
}{
	{"org.freedesktop.UPower.PowerProfiles", "/org/freedesktop/UPower/PowerProfiles", "org.freedesktop.UPower.PowerProfiles"},
	{"net.hadess.PowerProfiles", "/net/hadess/PowerProfiles", "net.hadess.PowerProfiles"},
}

// Battery is one battery as UPower reports it.
type Battery struct {
	ID         string  `json:"id"` // native name, e.g. BAT1
	Model      string  `json:"model,omitempty"`
	Vendor     string  `json:"vendor,omitempty"`
	Pct        float64 `json:"pct"`
	State      string  `json:"state"`  // charging, discharging, full, empty, pending-charge, pending-discharge, unknown
	RateW      float64 `json:"rate_w"` // charge/discharge power
	EnergyWh   float64 `json:"energy_wh"`
	FullWh     float64 `json:"full_wh"`
	DesignWh   float64 `json:"design_wh"`
	HealthPct  float64 `json:"health_pct"`           // full vs design capacity
	Cycles     int     `json:"cycles,omitempty"`     // 0 when unknown
	ToEmptySec int64   `json:"to_empty_s,omitempty"` // 0 when unknown / not discharging
	ToFullSec  int64   `json:"to_full_s,omitempty"`
}

// Profile is the power-profiles-daemon state.
type Profile struct {
	Active    string   `json:"active"`
	Available []string `json:"available"`
	// Degraded explains why "performance" is throttled (e.g. "lap-detected").
	Degraded string `json:"degraded,omitempty"`
}

// State is a snapshot for the sampler and the API.
type State struct {
	Batteries []Battery `json:"batteries"`
	OnAC      *bool     `json:"on_ac,omitempty"`
	Profile   *Profile  `json:"profile,omitempty"`
}

// Dialer opens a D-Bus connection (dbusx.SystemBus in production).
type Dialer func(ctx context.Context) (dbusx.Bus, error)

// Watcher keeps State current from D-Bus.
type Watcher struct {
	dial Dialer
	log  *slog.Logger

	mu        sync.Mutex
	bus       dbusx.Bus // nil while disconnected
	batteries map[dbus.ObjectPath]Battery
	lines     map[dbus.ObjectPath]bool // line power -> online
	profile   *Profile
	ppd       int // index into ppdNames, -1 if not available
}

// NewWatcher returns a Watcher; call Run to connect and follow changes.
func NewWatcher(dial Dialer, log *slog.Logger) *Watcher {
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{dial: dial, log: log, batteries: map[dbus.ObjectPath]Battery{}, lines: map[dbus.ObjectPath]bool{}, ppd: -1}
}

// reconnectDelay is how long Run waits before redialling a lost bus.
var reconnectDelay = 5 * time.Second

// Run connects, loads the initial state and applies D-Bus signals until ctx
// ends, reconnecting if the bus goes away. A missing UPower or
// power-profiles-daemon is not an error: that part of State stays empty.
func (w *Watcher) Run(ctx context.Context) {
	warned := false
	for ctx.Err() == nil {
		bus, err := w.dial(ctx)
		if err != nil {
			if !warned {
				w.log.Warn("power: system bus unavailable", "err", err)
				warned = true
			}
		} else {
			warned = false
			w.mu.Lock()
			w.bus = bus
			w.mu.Unlock()
			if err := w.follow(ctx, bus); err != nil && ctx.Err() == nil {
				w.log.Warn("power: lost system bus, reconnecting", "err", err)
			}
			w.mu.Lock()
			w.bus = nil
			w.mu.Unlock()
			if c, ok := bus.(interface{ Close() error }); ok {
				_ = c.Close()
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(reconnectDelay):
		}
	}
}

// Snapshot returns the current state.
func (w *Watcher) Snapshot() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := State{Batteries: make([]Battery, 0, len(w.batteries))}
	for _, b := range w.batteries {
		st.Batteries = append(st.Batteries, b)
	}
	sort.Slice(st.Batteries, func(i, j int) bool { return st.Batteries[i].ID < st.Batteries[j].ID })
	if len(w.lines) > 0 {
		on := false
		for _, online := range w.lines {
			on = on || online
		}
		st.OnAC = &on
	}
	if w.profile != nil {
		p := *w.profile
		p.Available = slices.Clone(p.Available)
		st.Profile = &p
	}
	return st
}

// follow loads state from bus and applies its signals until ctx ends or the
// connection drops.
func (w *Watcher) follow(ctx context.Context, bus dbusx.Bus) error {
	sigs, err := bus.Watch(ctx, dbus.WithMatchPathNamespace(upowerPath))
	if err != nil {
		return err
	}
	var ppdSigs <-chan *dbus.Signal
	w.loadDevices(ctx, bus)
	if w.loadProfile(ctx, bus) {
		if ppdSigs, err = bus.Watch(ctx, dbus.WithMatchObjectPath(ppdNames[w.ppdIndex()].path)); err != nil {
			w.log.Warn("power: cannot follow profile changes", "err", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-sigs:
			if !ok {
				return errors.New("power: D-Bus connection closed")
			}
			w.handleUPower(ctx, bus, s)
		case s, ok := <-ppdSigs:
			if !ok {
				ppdSigs = nil
				continue
			}
			if s.Name == propsIface+".PropertiesChanged" {
				w.loadProfile(ctx, bus)
			}
		}
	}
}

func (w *Watcher) ppdIndex() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ppd
}

func (w *Watcher) handleUPower(ctx context.Context, bus dbusx.Bus, s *dbus.Signal) {
	switch s.Name {
	case upowerIface + ".DeviceAdded", upowerIface + ".DeviceRemoved":
		w.loadDevices(ctx, bus)
	case propsIface + ".PropertiesChanged":
		if strings.HasPrefix(string(s.Path), string(upowerPath)+"/devices/") {
			w.loadDevice(ctx, bus, s.Path)
		}
	}
}

func (w *Watcher) loadDevices(ctx context.Context, bus dbusx.Bus) {
	var paths []dbus.ObjectPath
	if err := bus.Call(ctx, upowerDest, upowerPath, upowerIface+".EnumerateDevices", []any{&paths}); err != nil {
		w.log.Warn("power: UPower unavailable", "err", err)
		return
	}
	w.mu.Lock()
	clear(w.batteries)
	clear(w.lines)
	w.mu.Unlock()
	for _, p := range paths {
		w.loadDevice(ctx, bus, p)
	}
}

func (w *Watcher) loadDevice(ctx context.Context, bus dbusx.Bus, path dbus.ObjectPath) {
	props, err := bus.GetAll(ctx, upowerDest, path, deviceIface)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		delete(w.batteries, path) // device vanished
		delete(w.lines, path)
		return
	}
	switch get[uint32](props, "Type") {
	case upowerTypeBattery:
		if get[bool](props, "PowerSupply") && get[bool](props, "IsPresent") {
			w.batteries[path] = parseBattery(props)
		} else {
			delete(w.batteries, path) // peripheral (mouse) or removed
		}
	case upowerTypeLinePower:
		w.lines[path] = get[bool](props, "Online")
	}
}

// loadProfile reads the profile from the first ppd bus name that answers.
func (w *Watcher) loadProfile(ctx context.Context, bus dbusx.Bus) bool {
	order := []int{0, 1}
	if i := w.ppdIndex(); i >= 0 {
		order = []int{i}
	}
	for _, i := range order {
		n := ppdNames[i]
		props, err := bus.GetAll(ctx, n.dest, n.path, n.iface)
		if err != nil {
			continue
		}
		p := parseProfile(props)
		w.mu.Lock()
		w.profile, w.ppd = &p, i
		w.mu.Unlock()
		return true
	}
	return false
}

// ErrUnknownProfile is returned for a profile the daemon does not offer.
var ErrUnknownProfile = errors.New("power: unknown profile")

// ErrNoProfiles is returned when power-profiles-daemon is not running.
var ErrNoProfiles = errors.New("power: power-profiles-daemon not available")

// SetProfile switches the active profile. The name must be one the daemon
// advertises; polkit (ppd's own policy) decides whether the user may.
func (w *Watcher) SetProfile(ctx context.Context, name string) error {
	w.mu.Lock()
	idx, prof, bus := w.ppd, w.profile, w.bus
	w.mu.Unlock()
	if idx < 0 || prof == nil || bus == nil {
		return ErrNoProfiles
	}
	if !slices.Contains(prof.Available, name) {
		return fmt.Errorf("%w %q", ErrUnknownProfile, name)
	}
	n := ppdNames[idx]
	if err := bus.Set(ctx, n.dest, n.path, n.iface, "ActiveProfile", name); err != nil {
		return err
	}
	w.loadProfile(ctx, bus) // don't wait for the signal
	return nil
}

var upowerStates = map[uint32]string{
	1: "charging", 2: "discharging", 3: "empty", 4: "full", 5: "pending-charge", 6: "pending-discharge",
}

func parseBattery(p map[string]dbus.Variant) Battery {
	b := Battery{
		ID:         get[string](p, "NativePath"),
		Model:      get[string](p, "Model"),
		Vendor:     get[string](p, "Vendor"),
		Pct:        get[float64](p, "Percentage"),
		RateW:      get[float64](p, "EnergyRate"),
		EnergyWh:   get[float64](p, "Energy"),
		FullWh:     get[float64](p, "EnergyFull"),
		DesignWh:   get[float64](p, "EnergyFullDesign"),
		ToEmptySec: get[int64](p, "TimeToEmpty"),
		ToFullSec:  get[int64](p, "TimeToFull"),
	}
	b.State = upowerStates[get[uint32](p, "State")]
	if b.State == "" {
		b.State = "unknown"
	}
	if c := get[int32](p, "ChargeCycles"); c > 0 {
		b.Cycles = int(c)
	}
	switch {
	case get[float64](p, "Capacity") > 0:
		b.HealthPct = get[float64](p, "Capacity")
	case b.DesignWh > 0:
		b.HealthPct = b.FullWh / b.DesignWh * 100
	}
	b.HealthPct = min(b.HealthPct, 100)
	return b
}

func parseProfile(p map[string]dbus.Variant) Profile {
	prof := Profile{Active: get[string](p, "ActiveProfile"), Degraded: get[string](p, "PerformanceDegraded")}
	for _, m := range get[[]map[string]dbus.Variant](p, "Profiles") {
		if name := get[string](m, "Profile"); name != "" {
			prof.Available = append(prof.Available, name)
		}
	}
	return prof
}

// get returns props[key] as T, or T's zero value if missing or mistyped.
func get[T any](props map[string]dbus.Variant, key string) T {
	var zero T
	v, ok := props[key]
	if !ok {
		return zero
	}
	t, ok := v.Value().(T)
	if !ok {
		return zero
	}
	return t
}
