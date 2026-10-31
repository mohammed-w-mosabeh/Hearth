// Package settings implements the Settings page (SPEC 12, ROADMAP 4.2),
// only through D-Bus services (SECURITY S9) and, for audio, PipeWire's
// own tools through safeexec: hostname (hostnamed), time zone and NTP
// (timedated), brightness and power actions (logind), Wi-Fi and
// connections (NetworkManager), audio (pw-dump, wpctl) and autostart
// entries. hearthd is not root: privileged changes are authorized by
// polkit, which shows the desktop's dialog (the calls allow interactive
// authorization).
package settings

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	hostnamed     = "org.freedesktop.hostname1"
	hostnamedPath = dbus.ObjectPath("/org/freedesktop/hostname1")
	timedated     = "org.freedesktop.timedate1"
	timedatedPath = dbus.ObjectPath("/org/freedesktop/timedate1")
	logind        = "org.freedesktop.login1"
	logindPath    = dbus.ObjectPath("/org/freedesktop/login1")
	logindManager = "org.freedesktop.login1.Manager"
)

// Bus is a system bus connection (dbusx.Conn in production).
type Bus interface {
	dbusx.Bus
	CallInteractive(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Close() error
}

// Reasons a change failed; the UI translates them.
const (
	ReasonUnauthorized = "unauthorized" // polkit refused or the dialog was cancelled
	ReasonUnavailable  = "unavailable"  // the service or device is not there
	ReasonError        = "error"        // anything else (Detail: the error)
)

// Hostname is hostnamed's state.
type Hostname struct {
	Static    string `json:"static"`
	Pretty    string `json:"pretty"`
	Transient string `json:"transient"`
	Chassis   string `json:"chassis,omitempty"`
}

// Time is timedated's state.
type Time struct {
	Timezone string `json:"timezone"`
	NTP      bool   `json:"ntp"`
	CanNTP   bool   `json:"can_ntp"`
	NTPSync  bool   `json:"ntp_synchronized"`
	LocalRTC bool   `json:"local_rtc"`
}

// Power lists the power actions logind offers ("yes", "challenge" mean
// possible, the latter with a password).
type Power struct {
	PowerOff  bool `json:"poweroff"`
	Reboot    bool `json:"reboot"`
	Suspend   bool `json:"suspend"`
	Hibernate bool `json:"hibernate"`
}

// Backlight is one display backlight from sysfs.
type Backlight struct {
	Name    string `json:"name"`
	Value   int    `json:"value"`
	Max     int    `json:"max"`
	Percent int    `json:"percent"`
}

// System is the GET /api/v1/system/info answer; a part whose service is
// missing is null.
type System struct {
	Hostname   *Hostname   `json:"hostname"`
	Time       *Time       `json:"time"`
	Power      *Power      `json:"power"`
	Backlights []Backlight `json:"backlights"`
}

// Config configures the settings module.
type Config struct {
	System func(ctx context.Context) (Bus, error)
	// Backlight is the sysfs class directory; default /sys/class/backlight.
	Backlight string
	// Audio runs pw-dump and wpctl (safeexec.Run in production).
	Audio Runner
	// Autostart lists and changes autostart entries.
	Autostart Autostart
}

// Settings serves the settings API and actions.
type Settings struct{ cfg Config }

// New returns a Settings.
func New(cfg Config) *Settings {
	if cfg.Backlight == "" {
		cfg.Backlight = "/sys/class/backlight"
	}
	return &Settings{cfg: cfg}
}

// Info reads hostname, time, power and backlight state. A missing part
// is left nil rather than failing the whole answer.
func (s *Settings) Info(ctx context.Context) System {
	out := System{Backlights: s.backlights()}
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return out
	}
	defer func() { _ = bus.Close() }()
	if p, err := bus.GetAll(ctx, hostnamed, hostnamedPath, hostnamed); err == nil {
		out.Hostname = &Hostname{Static: str(p, "StaticHostname"), Pretty: str(p, "PrettyHostname"),
			Transient: str(p, "Hostname"), Chassis: str(p, "Chassis")}
	}
	if p, err := bus.GetAll(ctx, timedated, timedatedPath, timedated); err == nil {
		out.Time = &Time{Timezone: str(p, "Timezone"), NTP: boolean(p, "NTP"), CanNTP: boolean(p, "CanNTP"),
			NTPSync: boolean(p, "NTPSynchronized"), LocalRTC: boolean(p, "LocalRTC")}
	}
	pw := &Power{}
	ok := false
	for _, c := range []struct {
		method string
		dst    *bool
	}{{"CanPowerOff", &pw.PowerOff}, {"CanReboot", &pw.Reboot}, {"CanSuspend", &pw.Suspend}, {"CanHibernate", &pw.Hibernate}} {
		var v string
		if err := bus.Call(ctx, logind, logindPath, logindManager+"."+c.method, []any{&v}); err == nil {
			ok = true
			*c.dst = v == "yes" || v == "challenge"
		}
	}
	if ok {
		out.Power = pw
	}
	return out
}

func str(p map[string]dbus.Variant, k string) string {
	s, _ := p[k].Value().(string)
	return s
}

func boolean(p map[string]dbus.Variant, k string) bool {
	b, _ := p[k].Value().(bool)
	return b
}

// Timezones lists the zones timedated accepts.
func (s *Settings) Timezones(ctx context.Context) ([]string, error) {
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bus.Close() }()
	var zones []string
	if err := bus.Call(ctx, timedated, timedatedPath, timedated+".ListTimezones", []any{&zones}); err != nil {
		return nil, err
	}
	return zones, nil
}

// backlightNameRe is a sysfs backlight device name.
var backlightNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func (s *Settings) backlights() []Backlight {
	out := []Backlight{}
	entries, err := os.ReadDir(s.cfg.Backlight)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if b, ok := s.backlight(e.Name()); ok {
			out = append(out, b)
		}
	}
	return out
}

func (s *Settings) backlight(name string) (Backlight, bool) {
	if !backlightNameRe.MatchString(name) {
		return Backlight{}, false
	}
	read := func(f string) (int, bool) {
		b, err := os.ReadFile(filepath.Join(s.cfg.Backlight, name, f)) // #nosec G304 -- name validated, under the sysfs class dir
		if err != nil {
			return 0, false
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return v, err == nil && v >= 0
	}
	v, ok1 := read("brightness")
	m, ok2 := read("max_brightness")
	if !ok1 || !ok2 || m == 0 {
		return Backlight{}, false
	}
	return Backlight{Name: name, Value: v, Max: m, Percent: (v*100 + m/2) / m}, true
}

// Validators ----------------------------------------------------------

// hostLabelRe is one label of a static hostname (RFC 1123, lower case
// as hostnamed stores it).
var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether s can be a static hostname: dot-separated
// labels, 64 bytes at most (hostnamed's limit).
func ValidHostname(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !hostLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// ValidPretty reports whether s can be a pretty hostname: free text
// without control characters, 64 bytes at most.
func ValidPretty(s string) bool {
	return len(s) <= 64 && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) == s
}

// tzRe is the shape of an IANA zone name ("Europe/Berlin",
// "America/Argentina/Buenos_Aires", "UTC", "Etc/GMT+3").
var tzRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]{0,63}(/[A-Za-z0-9][A-Za-z0-9_+-]{0,63}){0,2}$`)

// ValidTimezone reports whether s has the shape of a zone name; the plan
// also checks timedated lists it.
func ValidTimezone(s string) bool { return tzRe.MatchString(s) }

// Actions -------------------------------------------------------------

// Register adds the settings actions to reg.
func (s *Settings) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "system", Name: "hostname", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			v, ok := r.Params["value"]
			if len(r.Params) != 1 || !ok {
				return errors.New(`params must be {"value": "..."}`)
			}
			switch r.Target {
			case "static":
				if !ValidHostname(v) {
					return errors.New("a hostname is letters a-z, digits and hyphens, in dot-separated parts, 64 characters at most")
				}
			case "pretty":
				if !ValidPretty(v) {
					return errors.New("the name must be 64 bytes at most, without control characters or surrounding spaces")
				}
			default:
				return errors.New(`target must be "static" or "pretty"`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return one("set "+r.Target+" hostname to "+r.Params["value"], r.Target, r.Params["value"]), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			method := map[string]string{"static": "SetStaticHostname", "pretty": "SetPrettyHostname"}[r.Target]
			return s.call(ctx, p, hostnamed, hostnamedPath, hostnamed+"."+method, r.Params["value"], true)
		}})

	reg.Register(actions.Action{Kind: "system", Name: "timezone", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 || !ValidTimezone(r.Target) {
				return errors.New("target must be a time zone name and params empty")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			zones, err := s.Timezones(ctx)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "timedated is not available: %v", err)
			}
			if !contains(zones, r.Target) {
				return actions.Plan{}, actions.Errorf(http.StatusUnprocessableEntity, "unknown time zone %s", r.Target)
			}
			return one("set the time zone to "+r.Target, "timezone", r.Target), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.call(ctx, p, timedated, timedatedPath, timedated+".SetTimezone", r.Target, true)
		}})

	reg.Register(actions.Action{Kind: "system", Name: "ntp", Risk: actions.Reversible,
		Validate: onOff,
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return one("turn network time "+r.Target, "ntp", r.Target), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.call(ctx, p, timedated, timedatedPath, timedated+".SetNTP", r.Target == "on", true)
		}})

	reg.Register(actions.Action{Kind: "system", Name: "brightness", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			pct, err := strconv.Atoi(r.Params["percent"])
			if len(r.Params) != 1 || err != nil || pct < 1 || pct > 100 || !backlightNameRe.MatchString(r.Target) {
				return errors.New(`target must be a backlight and params {"percent": "1".."100"}`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			if _, ok := s.backlight(r.Target); !ok {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no backlight %s", r.Target)
			}
			return one("set "+r.Target+" to "+r.Params["percent"]+"%", r.Target, r.Params["percent"]+"%"), nil
		},
		Run: s.setBrightness})

	for _, o := range []struct{ name, method, verb string }{
		{"poweroff", "PowerOff", "power off"}, {"reboot", "Reboot", "restart"},
		{"suspend", "Suspend", "suspend"}, {"hibernate", "Hibernate", "hibernate"},
	} {
		reg.Register(actions.Action{Kind: "power", Name: o.name, Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if r.Target != "machine" || len(r.Params) != 0 {
					return errors.New(`target must be "machine" and params empty`)
				}
				return nil
			},
			Plan: func(ctx context.Context, _ actions.Request) (actions.Plan, error) { return s.planPower(ctx, o.verb) },
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				// logind asks polkit; interactive=true lets it ask the user.
				return s.call(ctx, p, logind, logindPath, logindManager+"."+o.method, true, false)
			}})
	}

	s.registerNetwork(reg)
	s.registerAudio(reg)
	s.registerAutostart(reg)
}

func onOff(r actions.Request) error {
	if (r.Target != "on" && r.Target != "off") || len(r.Params) != 0 {
		return errors.New(`target must be "on" or "off" and params empty`)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// one is a plan with one item.
func one(summary, id, name string) actions.Plan {
	return actions.Plan{Summary: summary, Items: []actions.Item{{ID: id, Name: name}}, Blocked: []actions.Blocked{}}
}

// call makes one D-Bus method call for the plan's single item. With
// interactiveArg, the method takes logind/hostnamed's trailing
// "interactive" boolean, set to true.
func (s *Settings) call(ctx context.Context, p actions.Plan, dest string, path dbus.ObjectPath, method string, arg any, interactiveArg bool) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return res, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	args := []any{arg}
	if interactiveArg {
		args = append(args, true)
	}
	if err := bus.CallInteractive(ctx, dest, path, method, nil, args...); err != nil {
		res.Failed = append(res.Failed, Failure(p.Items[0], err))
		return res, nil
	}
	res.Done = 1
	return res, nil
}

// Failure turns an error into a failed item with a reason.
func Failure(it actions.Item, err error) actions.Blocked {
	switch dbusx.ErrorName(err) {
	case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired",
		"org.freedesktop.PolicyKit1.Error.NotAuthorized", "org.freedesktop.PolicyKit1.Error.Cancelled":
		return actions.Blocked{Item: it, Reason: ReasonUnauthorized}
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.UnknownMethod":
		return actions.Blocked{Item: it, Reason: ReasonUnavailable}
	}
	var de dbus.Error
	if errors.As(err, &de) && len(de.Body) > 0 {
		it.Detail = fmt.Sprint(de.Body[0])
	} else {
		it.Detail = err.Error()
	}
	return actions.Blocked{Item: it, Reason: ReasonError}
}

// inhibitor is one entry of ListInhibitors' a(ssssuu).
type inhibitor struct {
	What, Who, Why, Mode string
	UID, PID             uint32
}

// planPower names the machine and the apps that delay or block the
// action (logind's inhibitors), so the dialog can show them.
func (s *Settings) planPower(ctx context.Context, verb string) (actions.Plan, error) {
	host, _ := os.Hostname()
	it := actions.Item{ID: "machine", Name: host}
	p := actions.Plan{Summary: verb + " this computer", Items: []actions.Item{it}, Blocked: []actions.Blocked{}}
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	var inh []inhibitor
	if err := bus.Call(ctx, logind, logindPath, logindManager+".ListInhibitors", []any{&inh}); err != nil {
		return p, nil //nolint:nilerr // the inhibitor list is informational
	}
	what := "shutdown"
	if verb == "suspend" || verb == "hibernate" {
		what = "sleep"
	}
	var notes []string
	for _, i := range inh {
		if !contains(strings.Split(i.What, ":"), what) || i.Who == "" {
			continue
		}
		note := i.Who
		if i.Why != "" {
			note += " (" + i.Why + ")"
		}
		if i.Mode == "block" {
			note += " blocks it"
		}
		notes = append(notes, note)
	}
	if len(notes) > 0 {
		p.Items[0].Detail = strings.Join(notes, "; ")
	}
	return p, nil
}

// setBrightness asks logind to set the backlight for the user's
// graphical session: no polkit, as only that session's owner may.
func (s *Settings) setBrightness(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	b, ok := s.backlight(r.Target)
	if !ok {
		res.Failed = append(res.Failed, actions.Blocked{Item: p.Items[0], Reason: ReasonUnavailable})
		return res, nil
	}
	pct, _ := strconv.Atoi(r.Params["percent"])
	raw := (b.Max*pct + 50) / 100
	if raw < 0 || raw > math.MaxUint32 {
		res.Failed = append(res.Failed, actions.Blocked{Item: p.Items[0], Reason: ReasonUnavailable})
		return res, nil
	}
	value := uint32(raw)
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return res, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	var display struct {
		ID   string
		Path dbus.ObjectPath
	}
	props, err := bus.GetAll(ctx, logind, logindPath+"/user/self", "org.freedesktop.login1.User")
	if err == nil {
		err = dbus.Store([]any{props["Display"].Value()}, &display)
	}
	if err != nil || display.Path == "" || display.Path == "/" {
		res.Failed = append(res.Failed, actions.Blocked{Item: p.Items[0], Reason: ReasonUnavailable})
		return res, nil
	}
	if err := bus.Call(ctx, logind, display.Path, "org.freedesktop.login1.Session.SetBrightness", nil, "backlight", r.Target, value); err != nil {
		res.Failed = append(res.Failed, Failure(p.Items[0], err))
		return res, nil
	}
	res.Done = 1
	return res, nil
}
