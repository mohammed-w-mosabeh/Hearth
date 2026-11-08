package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

type call struct {
	path        dbus.ObjectPath
	method      string
	args        []any
	interactive bool
}

// fakeBus plays the system bus: properties by "path|iface", replies by
// "path|method" or "method".
type fakeBus struct {
	mu      sync.Mutex
	props   map[string]map[string]dbus.Variant
	replies map[string][]any
	errs    map[string]error
	calls   []call
}

func newBus() *fakeBus {
	return &fakeBus{props: map[string]map[string]dbus.Variant{}, replies: map[string][]any{}, errs: map[string]error{}}
}

func (b *fakeBus) GetAll(_ context.Context, _ string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	p, ok := b.props[string(path)+"|"+iface]
	if !ok {
		return nil, dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}
	}
	return p, nil
}

func (b *fakeBus) call(path dbus.ObjectPath, method string, out []any, interactive bool, args []any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, call{path: path, method: method, args: args, interactive: interactive})
	for _, k := range []string{string(path) + "|" + method, method} {
		if err, ok := b.errs[k]; ok {
			return err
		}
		if r, ok := b.replies[k]; ok {
			for i := range out {
				if i < len(r) {
					reflect.ValueOf(out[i]).Elem().Set(reflect.ValueOf(r[i]))
				}
			}
			return nil
		}
	}
	if len(out) > 0 {
		return dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}
	}
	return nil
}

func (b *fakeBus) Call(_ context.Context, _ string, path dbus.ObjectPath, method string, out []any, args ...any) error {
	return b.call(path, method, out, false, args)
}

func (b *fakeBus) CallInteractive(_ context.Context, _ string, path dbus.ObjectPath, method string, out []any, args ...any) error {
	return b.call(path, method, out, true, args)
}

func (b *fakeBus) Set(context.Context, string, dbus.ObjectPath, string, string, any) error {
	return errors.New("unused")
}

func (b *fakeBus) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	return nil, errors.New("unused")
}

func (b *fakeBus) Close() error { return nil }

func (b *fakeBus) last(method string) (call, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.calls) - 1; i >= 0; i-- {
		if strings.HasSuffix(b.calls[i].method, method) {
			return b.calls[i], true
		}
	}
	return call{}, false
}

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

func newSettings(t *testing.T, bus *fakeBus) *Settings {
	t.Helper()
	dir := t.TempDir()
	for name, val := range map[string]string{"intel_backlight/brightness": "750\n", "intel_backlight/max_brightness": "1500\n", "bad/brightness": "x"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(val), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return New(Config{System: func(context.Context) (Bus, error) { return bus, nil }, Backlight: dir})
}

// do runs action kind/name through a registry, as the API does.
func do(t *testing.T, s *Settings, kind, name, target string, params map[string]string) (actions.Plan, actions.Result, error) {
	t.Helper()
	reg := actions.NewRegistry(nil)
	s.Register(reg)
	a, ok := reg.Lookup(kind + "/" + name)
	if !ok {
		t.Fatalf("no action %s/%s", kind, name)
	}
	r := actions.Request{Target: target, Params: params}
	if params == nil {
		r.Params = map[string]string{}
	}
	if err := a.Validate(r); err != nil {
		return actions.Plan{}, actions.Result{}, err
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		return p, actions.Result{}, err
	}
	res, err := a.Run(context.Background(), r, p)
	return p, res, err
}

func TestInfo(t *testing.T) {
	bus := newBus()
	bus.props["/org/freedesktop/hostname1|"+hostnamed] = map[string]dbus.Variant{"StaticHostname": v("debian"), "PrettyHostname": v("My PC"), "Hostname": v("debian"), "Chassis": v("laptop")}
	bus.props["/org/freedesktop/timedate1|"+timedated] = map[string]dbus.Variant{"Timezone": v("Asia/Amman"), "NTP": v(true), "CanNTP": v(true)}
	bus.replies["org.freedesktop.login1.Manager.CanPowerOff"] = []any{"yes"}
	bus.replies["org.freedesktop.login1.Manager.CanReboot"] = []any{"challenge"}
	bus.replies["org.freedesktop.login1.Manager.CanSuspend"] = []any{"no"}
	bus.replies["org.freedesktop.login1.Manager.CanHibernate"] = []any{"na"}
	got := newSettings(t, bus).Info(context.Background())
	if got.Hostname == nil || got.Hostname.Pretty != "My PC" || got.Time == nil || got.Time.Timezone != "Asia/Amman" || !got.Time.NTP {
		t.Fatalf("%+v %+v", got.Hostname, got.Time)
	}
	if *got.Power != (Power{PowerOff: true, Reboot: true}) {
		t.Errorf("power %+v", *got.Power)
	}
	if len(got.Backlights) != 1 || got.Backlights[0] != (Backlight{Name: "intel_backlight", Value: 750, Max: 1500, Percent: 50}) {
		t.Errorf("backlights %+v", got.Backlights)
	}
	// Nothing answers: every part is missing, nothing fails.
	if got := newSettings(t, newBus()).Info(context.Background()); got.Hostname != nil || got.Time != nil || got.Power != nil {
		t.Errorf("empty bus %+v", got)
	}
}

func TestSystemActions(t *testing.T) {
	bus := newBus()
	s := newSettings(t, bus)
	if _, res, err := do(t, s, "system", "hostname", "static", map[string]string{"value": "box-2"}); err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	c, _ := bus.last("SetStaticHostname")
	if !c.interactive || !reflect.DeepEqual(c.args, []any{"box-2", true}) {
		t.Errorf("call %+v", c)
	}
	for _, bad := range []map[string]string{{"value": "Box"}, {"value": "-x"}, {"value": "a..b"}, {"value": strings.Repeat("a", 65)}, {"value": "x", "y": "z"}, {}} {
		if _, _, err := do(t, s, "system", "hostname", "static", bad); err == nil {
			t.Errorf("hostname %v accepted", bad)
		}
	}
	if _, _, err := do(t, s, "system", "hostname", "pretty", map[string]string{"value": "Mo's PC"}); err != nil {
		t.Error(err)
	}
	if _, _, err := do(t, s, "system", "hostname", "pretty", map[string]string{"value": "a\nb"}); err == nil {
		t.Error("control character accepted")
	}

	bus.replies[timedated+".ListTimezones"] = []any{[]string{"Asia/Amman", "UTC"}}
	if _, res, err := do(t, s, "system", "timezone", "Asia/Amman", nil); err != nil || res.Done != 1 {
		t.Errorf("timezone %+v %v", res, err)
	}
	if _, _, err := do(t, s, "system", "timezone", "Mars/Olympus", nil); err == nil || !strings.Contains(err.Error(), "unknown time zone") {
		t.Errorf("unlisted zone: %v", err)
	}
	if _, _, err := do(t, s, "system", "ntp", "on", nil); err != nil {
		t.Error(err)
	}
	if c, _ := bus.last("SetNTP"); !reflect.DeepEqual(c.args, []any{true, true}) {
		t.Errorf("SetNTP %+v", c.args)
	}

	// polkit says no: a failed item, not an error.
	bus.errs[hostnamed+".SetStaticHostname"] = dbus.Error{Name: "org.freedesktop.PolicyKit1.Error.NotAuthorized"}
	if _, res, err := do(t, s, "system", "hostname", "static", map[string]string{"value": "x"}); err != nil || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnauthorized {
		t.Errorf("denied %+v %v", res, err)
	}
}

func TestPower(t *testing.T) {
	bus := newBus()
	s := newSettings(t, bus)
	bus.replies[logindManager+".ListInhibitors"] = []any{[]inhibitor{
		{What: "shutdown:sleep", Who: "Firefox", Why: "Downloading", Mode: "delay"},
		{What: "sleep", Who: "Steam", Why: "Updating", Mode: "block"},
		{What: "idle", Who: "Video", Mode: "block"},
	}}
	p, res, err := do(t, s, "power", "reboot", "machine", nil)
	if err != nil || res.Done != 1 || p.Items[0].Detail != "Firefox (Downloading)" {
		t.Fatalf("%+v %+v %v", p, res, err)
	}
	if c, _ := bus.last("Reboot"); !c.interactive || !reflect.DeepEqual(c.args, []any{true}) {
		t.Errorf("Reboot %+v", c)
	}
	if p, _, _ := do(t, s, "power", "suspend", "machine", nil); p.Items[0].Detail != "Firefox (Downloading); Steam (Updating) blocks it" {
		t.Errorf("suspend detail %q", p.Items[0].Detail)
	}
	if _, _, err := do(t, s, "power", "poweroff", "other", nil); err == nil {
		t.Error("bad target accepted")
	}
	reg := actions.NewRegistry(nil)
	s.Register(reg)
	if a, _ := reg.Lookup("power/poweroff"); a.Risk != actions.Destructive {
		t.Error("power off must need a confirmation")
	}
}

func TestBrightness(t *testing.T) {
	bus := newBus()
	s := newSettings(t, bus)
	bus.props["/org/freedesktop/login1/user/self|org.freedesktop.login1.User"] = map[string]dbus.Variant{
		"Display": v([]any{"2", dbus.ObjectPath("/org/freedesktop/login1/session/_32")})}
	if _, res, err := do(t, s, "system", "brightness", "intel_backlight", map[string]string{"percent": "30"}); err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	c, _ := bus.last("SetBrightness")
	if c.path != "/org/freedesktop/login1/session/_32" || !reflect.DeepEqual(c.args, []any{"backlight", "intel_backlight", uint32(450)}) {
		t.Errorf("call %+v", c)
	}
	for _, pct := range []string{"0", "101", "x", ""} {
		if _, _, err := do(t, s, "system", "brightness", "intel_backlight", map[string]string{"percent": pct}); err == nil {
			t.Errorf("percent %q accepted", pct)
		}
	}
	if _, _, err := do(t, s, "system", "brightness", "../x", map[string]string{"percent": "5"}); err == nil {
		t.Error("path accepted")
	}
	if _, _, err := do(t, s, "system", "brightness", "bad", map[string]string{"percent": "5"}); err == nil {
		t.Error("unreadable backlight planned")
	}
}

const (
	dev1 = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/1")
	dev2 = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/2")
	ap1  = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/1")
	ap2  = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/2")
	ap3  = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/3")
	ap4  = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/4")
	ac1  = dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/1")
	con1 = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings/1")
	con2 = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings/2")
)

func nmBus() *fakeBus {
	bus := newBus()
	bus.props[string(nmPath)+"|"+nm] = map[string]dbus.Variant{"WirelessEnabled": v(true), "WirelessHardwareEnabled": v(true),
		"ActiveConnections": v([]dbus.ObjectPath{ac1})}
	bus.props[string(ac1)+"|"+nmActive] = map[string]dbus.Variant{"Id": v("Home"), "Uuid": v("11111111-2222-3333-4444-555555555555")}
	bus.replies[nm+".GetDevices"] = []any{[]dbus.ObjectPath{dev1, dev2}}
	bus.props[string(dev1)+"|"+nmDevice] = map[string]dbus.Variant{"Interface": v("wlp2s0"), "DeviceType": v(uint32(2)), "State": v(uint32(100)), "ActiveConnection": v(ac1)}
	bus.props[string(dev1)+"|"+nmWireless] = map[string]dbus.Variant{"AccessPoints": v([]dbus.ObjectPath{ap1, ap2, ap3, ap4}), "ActiveAccessPoint": v(ap1)}
	bus.props[string(dev2)+"|"+nmDevice] = map[string]dbus.Variant{"Interface": v("docker0"), "DeviceType": v(uint32(13)), "State": v(uint32(100))}
	bus.props[string(ap1)+"|"+nmAP] = map[string]dbus.Variant{"Ssid": v([]byte("Home")), "Strength": v(byte(70)), "RsnFlags": v(uint32(0x188))}
	bus.props[string(ap2)+"|"+nmAP] = map[string]dbus.Variant{"Ssid": v([]byte("Cafe\x00<b>")), "Strength": v(byte(40))}
	bus.props[string(ap3)+"|"+nmAP] = map[string]dbus.Variant{"Ssid": v([]byte("Cafe\x00<b>")), "Strength": v(byte(55))}
	bus.props[string(ap4)+"|"+nmAP] = map[string]dbus.Variant{"Ssid": v([]byte("Corp")), "Strength": v(byte(90)), "RsnFlags": v(uint32(0x200))}
	bus.replies[nm+".Settings.ListConnections"] = []any{[]dbus.ObjectPath{con1, con2, "/org/freedesktop/NetworkManager/Settings/3"}}
	bus.replies["/org/freedesktop/NetworkManager/Settings/3|"+nmConn+".GetSettings"] = []any{map[string]map[string]dbus.Variant{
		"connection": {"id": v("docker0"), "uuid": v("bbbbbbbb-2222-3333-4444-555555555555"), "type": v("bridge")},
	}}
	bus.replies[string(con1)+"|"+nmConn+".GetSettings"] = []any{map[string]map[string]dbus.Variant{
		"connection":      {"id": v("Home"), "uuid": v("11111111-2222-3333-4444-555555555555"), "type": v("802-11-wireless")},
		"802-11-wireless": {"ssid": v([]byte("Home"))},
	}}
	bus.replies[string(con2)+"|"+nmConn+".GetSettings"] = []any{map[string]map[string]dbus.Variant{
		"connection": {"id": v("Office VPN"), "uuid": v("aaaaaaaa-2222-3333-4444-555555555555"), "type": v("vpn"), "autoconnect": v(false)},
	}}
	return bus
}

func TestNetwork(t *testing.T) {
	bus := nmBus()
	n := newSettings(t, bus).Network(context.Background())
	if !n.Available || !n.WifiEnabled || len(n.Devices) != 1 || n.Devices[0] != (NetDevice{Name: "wlp2s0", Type: "wifi", State: "connected", Connection: "Home"}) {
		t.Fatalf("%+v", n)
	}
	want := []AccessPoint{
		{SSID: "Home", Device: "wlp2s0", Strength: 70, Security: "psk", Active: true, Saved: true},
		{SSID: "Corp", Device: "wlp2s0", Strength: 90, Security: "enterprise"},
		{SSID: "Cafe�<b>", Device: "wlp2s0", Strength: 55, Security: "open"},
	}
	if !reflect.DeepEqual(n.AccessPoints, want) {
		t.Errorf("access points\n%+v\nwant\n%+v", n.AccessPoints, want)
	}
	if len(n.Connections) != 2 || !n.Connections[0].Active || n.Connections[0].SSID != "Home" || n.Connections[1].Type != "vpn" || n.Connections[1].Autoconnect {
		t.Errorf("connections %+v", n.Connections)
	}
	if got := newSettings(t, newBus()).Network(context.Background()); got.Available || got.Devices == nil {
		t.Errorf("no NetworkManager %+v", got)
	}
}

func TestNetworkActions(t *testing.T) {
	bus := nmBus()
	s := newSettings(t, bus)
	if _, res, err := do(t, s, "wifi", "radio", "off", nil); err != nil || res.Done != 1 {
		t.Fatal(res, err)
	}
	if c, _ := bus.last("Properties.Set"); !c.interactive || !reflect.DeepEqual(c.args, []any{nm, "WirelessEnabled", v(false)}) {
		t.Errorf("radio %+v", c)
	}
	if _, res, err := do(t, s, "wifi", "scan", "wlp2s0", nil); err != nil || res.Done != 1 {
		t.Error(res, err)
	}
	if _, _, err := do(t, s, "wifi", "scan", "docker0", nil); err == nil {
		t.Error("scan on a non-Wi-Fi device")
	}

	// join: no secret in the request; NM asks the desktop's agent.
	bus.replies[nm+".AddAndActivateConnection"] = []any{con2, ac1}
	if _, res, err := do(t, s, "wifi", "join", "Home", map[string]string{"device": "wlp2s0"}); err != nil || res.Done != 1 {
		t.Fatal(res, err)
	}
	c, _ := bus.last("AddAndActivateConnection")
	set := c.args[0].(map[string]map[string]dbus.Variant)
	if c.args[1] != dev1 || string(set["802-11-wireless"]["ssid"].Value().([]byte)) != "Home" ||
		set["802-11-wireless-security"]["key-mgmt"].Value() != "wpa-psk" || len(set["802-11-wireless-security"]) != 1 {
		t.Errorf("join %+v", c.args)
	}
	if _, _, err := do(t, s, "wifi", "join", "Cafe�<b>", map[string]string{"device": "wlp2s0"}); err != nil {
		t.Error(err)
	}
	if c, _ := bus.last("AddAndActivateConnection"); c.args[0].(map[string]map[string]dbus.Variant)["802-11-wireless-security"] != nil {
		t.Error("open network given security settings")
	}
	if p, res, err := do(t, s, "wifi", "join", "Corp", map[string]string{"device": "wlp2s0"}); err != nil || len(p.Blocked) != 1 || p.Blocked[0].Reason != "enterprise" || res.Done != 0 {
		t.Errorf("enterprise %+v %+v %v", p, res, err)
	}
	for _, bad := range []string{"", strings.Repeat("x", 33), "a\nb"} {
		if _, _, err := do(t, s, "wifi", "join", bad, map[string]string{"device": "wlp2s0"}); err == nil {
			t.Errorf("ssid %q accepted", bad)
		}
	}
	if _, _, err := do(t, s, "wifi", "join", "Far away", map[string]string{"device": "wlp2s0"}); err == nil {
		t.Error("network out of range planned")
	}

	bus.replies[nm+".Settings.GetConnectionByUuid"] = []any{con2}
	if p, res, err := do(t, s, "network", "connect", "aaaaaaaa-2222-3333-4444-555555555555", nil); err != nil || res.Done != 1 || p.Items[0].Name != "Office VPN" {
		t.Errorf("connect %+v %+v %v", p, res, err)
	}
	if c, _ := bus.last("ActivateConnection"); c.args[0] != con2 {
		t.Errorf("activate %+v", c.args)
	}
	if _, res, err := do(t, s, "network", "disconnect", "11111111-2222-3333-4444-555555555555", nil); err != nil || res.Done != 1 {
		t.Error(res, err)
	}
	if c, _ := bus.last("DeactivateConnection"); c.args[0] != ac1 {
		t.Errorf("deactivate %+v", c.args)
	}
	if _, _, err := do(t, s, "network", "disconnect", "aaaaaaaa-2222-3333-4444-555555555555", nil); err == nil {
		t.Error("inactive connection disconnected")
	}
	if _, _, err := do(t, s, "network", "connect", "not-a-uuid", nil); err == nil {
		t.Error("bad uuid accepted")
	}
}

const dump = `[
 {"id": 0, "type": "PipeWire:Interface:Core", "info": {}},
 {"id": 59, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.x", "node.description": "Built-in Audio"}}},
 {"id": 60, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Source", "node.name": "alsa_input.x", "node.description": "<b>Mic</b>"}}},
 {"id": 61, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "bt"}}},
 {"id": 70, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "node.name": "firefox"}}},
 {"id": 30, "type": "PipeWire:Interface:Metadata", "props": {"metadata.name": "default"}, "metadata": [
   {"subject": 0, "key": "default.audio.sink", "type": "Spa:String:JSON", "value": {"name": "alsa_output.x"}},
   {"subject": 0, "key": "default.configured.audio.sink", "value": {"name": "bt"}},
   {"subject": 0, "key": "default.audio.source", "value": {"name": "alsa_input.x"}}]}
]`

type fakeRunner struct {
	mu   sync.Mutex
	runs [][]string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (safeexec.Result, error) {
	if err := safeexec.Check(name, args...); err != nil {
		return safeexec.Result{}, err
	}
	f.mu.Lock()
	f.runs = append(f.runs, append([]string{name}, args...))
	f.mu.Unlock()
	switch {
	case name == "pw-dump":
		return safeexec.Result{Stdout: []byte(dump)}, nil
	case name == "wpctl-get-volume" && args[1] == "59":
		return safeexec.Result{Stdout: []byte("Volume: 0.40 [MUTED]\n")}, nil
	case name == "wpctl-get-volume":
		return safeexec.Result{Stdout: []byte("Volume: 1.00\n")}, nil
	}
	return safeexec.Result{}, nil
}

func TestAudio(t *testing.T) {
	f := &fakeRunner{}
	s := New(Config{System: func(context.Context) (Bus, error) { return newBus(), nil }, Audio: f.run})
	a := s.Audio(context.Background())
	want := []AudioDevice{
		{ID: 59, Name: "Built-in Audio", Kind: "output", Volume: 0.4, Muted: true, Default: true},
		{ID: 61, Name: "bt", Kind: "output", Volume: 1},
		{ID: 60, Name: "<b>Mic</b>", Kind: "input", Volume: 1, Default: true},
	}
	if !a.Available || !reflect.DeepEqual(a.Devices, want) {
		t.Fatalf("%+v", a)
	}
	if _, res, err := do(t, s, "audio", "volume", "59", map[string]string{"volume": "0.75"}); err != nil || res.Done != 1 {
		t.Fatal(res, err)
	}
	if last := f.runs[len(f.runs)-1]; !reflect.DeepEqual(last, []string{"wpctl-set-volume", "set-volume", "59", "0.75"}) {
		t.Errorf("ran %v", last)
	}
	if _, _, err := do(t, s, "audio", "mute", "60", map[string]string{"muted": "1"}); err != nil {
		t.Error(err)
	}
	if _, _, err := do(t, s, "audio", "default", "61", nil); err != nil {
		t.Error(err)
	}
	for _, bad := range []struct {
		name, target string
		params       map[string]string
	}{
		{"volume", "59", map[string]string{"volume": "1.51"}}, {"volume", "59", map[string]string{"volume": "2"}},
		{"volume", "59", map[string]string{"volume": "-0.10"}}, {"mute", "59", map[string]string{"muted": "yes"}},
		{"default", "70", nil}, {"default", "-1", nil}, {"volume", "59", nil},
	} {
		if _, _, err := do(t, s, "audio", bad.name, bad.target, bad.params); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, _, err := parseVolume("Volume: loud"); err == nil {
		t.Error("bad wpctl output parsed")
	}
}

type fakeAutostart struct {
	entries []discovery.AutostartEntry
	set     map[string]bool
	added   []string
}

func (f *fakeAutostart) List() []discovery.AutostartEntry { return f.entries }
func (f *fakeAutostart) Set(id string, on bool) error     { f.set[id] = on; return nil }
func (f *fakeAutostart) Add(id string) error {
	if id == "missing" {
		return discovery.ErrNoEntry
	}
	f.added = append(f.added, id)
	return nil
}

func TestAutostartActions(t *testing.T) {
	f := &fakeAutostart{entries: []discovery.AutostartEntry{{ID: "nm-applet", Name: "Network", Enabled: true, Origin: "system"}}, set: map[string]bool{}}
	s := New(Config{System: func(context.Context) (Bus, error) { return newBus(), nil }, Autostart: f})
	if p, res, err := do(t, s, "autostart", "set", "nm-applet", map[string]string{"enabled": "false"}); err != nil || res.Done != 1 || p.Summary != "stop starting Network at login" || f.set["nm-applet"] {
		t.Errorf("%+v %+v %v %v", p, res, err, f.set)
	}
	if _, _, err := do(t, s, "autostart", "set", "other", map[string]string{"enabled": "true"}); err == nil {
		t.Error("unlisted entry planned")
	}
	if _, _, err := do(t, s, "autostart", "set", "../x", map[string]string{"enabled": "true"}); err == nil {
		t.Error("path accepted")
	}
	if _, res, _ := do(t, s, "autostart", "add", "missing", nil); len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnavailable {
		t.Errorf("add missing %+v", res)
	}
}

func TestValidators(t *testing.T) {
	for _, ok := range []string{"debian", "box-2", "a.b.c", "x"} {
		if !ValidHostname(ok) {
			t.Errorf("hostname %q refused", ok)
		}
	}
	for _, ok := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+3", "America/Port-au-Prince"} {
		if !ValidTimezone(ok) {
			t.Errorf("zone %q refused", ok)
		}
	}
	for _, bad := range []string{"", "../etc/passwd", "/UTC", "Europe/", "Europe//Berlin", "a/b/c/d", "-x"} {
		if ValidTimezone(bad) {
			t.Errorf("zone %q accepted", bad)
		}
	}
}

func FuzzValidHostname(f *testing.F) {
	f.Add("debian")
	f.Add("a.b-c")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidHostname(s) && (len(s) > 64 || strings.ContainsAny(s, " /\\\x00\n_") || strings.HasPrefix(s, "-") || strings.Contains(s, "..")) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzValidTimezone(f *testing.F) {
	f.Add("Europe/Berlin")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidTimezone(s) && (strings.Contains(s, "..") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \\\x00\n")) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzValidSSID(f *testing.F) {
	f.Add("Home")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidSSID(s) && (len(s) > 32 || strings.ContainsAny(s, "\x00\n\r")) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func TestConnectionActive(t *testing.T) {
	s := newSettings(t, nmBus())
	ctx := context.Background()
	if on, err := s.ConnectionActive(ctx, "11111111-2222-3333-4444-555555555555"); err != nil || !on {
		t.Fatal(on, err)
	}
	if on, err := s.ConnectionActive(ctx, "aaaaaaaa-2222-3333-4444-555555555555"); err != nil || on {
		t.Fatal(on, err)
	}
	if _, err := s.ConnectionActive(ctx, "Home"); err == nil {
		t.Fatal("accepted a name")
	}
}

func TestActiveConnections(t *testing.T) {
	s := newSettings(t, nmBus())
	got, err := s.ActiveConnections(context.Background())
	if err != nil || len(got) != 1 || !got["11111111-2222-3333-4444-555555555555"] {
		t.Fatal(got, err)
	}
}
