package settings

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

const (
	nm             = "org.freedesktop.NetworkManager"
	nmPath         = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmSettingsPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	nmDevice       = nm + ".Device"
	nmWireless     = nm + ".Device.Wireless"
	nmAP           = nm + ".AccessPoint"
	nmActive       = nm + ".Connection.Active"
	nmConn         = nm + ".Settings.Connection"

	nmDeviceEthernet = 1
	nmDeviceWifi     = 2

	// 802.11 key management flags (NM80211ApSecurityFlags).
	nmKeyMgmtPSK   = 0x100
	nmKeyMgmt8021X = 0x200
	nmKeyMgmtSAE   = 0x400
	nmKeyMgmtOWE   = 0x800
	nmApPrivacy    = 0x1 // NM80211ApFlags: WEP or any encryption
)

// NetDevice is one network device NetworkManager manages.
type NetDevice struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // wifi, ethernet, other
	State      string `json:"state"`
	Connection string `json:"connection,omitempty"` // active connection's name
}

// AccessPoint is one Wi-Fi network in range (the strongest access point
// per name).
type AccessPoint struct {
	SSID     string `json:"ssid"`
	Device   string `json:"device"`
	Strength int    `json:"strength"`
	Security string `json:"security"` // open, psk, enterprise
	Active   bool   `json:"active"`
	Saved    bool   `json:"saved"` // a saved connection names it
}

// Connection is one saved connection profile.
type Connection struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	SSID        string `json:"ssid,omitempty"`
	Active      bool   `json:"active"`
	Autoconnect bool   `json:"autoconnect"`
}

// Network is the GET /api/v1/system/network answer.
type Network struct {
	Available    bool          `json:"available"` // NetworkManager answered
	WifiEnabled  bool          `json:"wifi_enabled"`
	WifiHardware bool          `json:"wifi_hardware"` // not blocked by a switch
	Devices      []NetDevice   `json:"devices"`
	AccessPoints []AccessPoint `json:"access_points"`
	Connections  []Connection  `json:"connections"`
}

var deviceStates = map[uint32]string{10: "unmanaged", 20: "unavailable", 30: "disconnected", 40: "connecting",
	50: "connecting", 60: "connecting", 70: "connecting", 80: "connecting", 90: "connecting", 100: "connected",
	110: "disconnecting", 120: "failed"}

// nmDev is what a device is needed for internally.
type nmDev struct {
	info NetDevice
	obj  dbus.ObjectPath
	typ  uint32
	aps  []dbus.ObjectPath
	ap   dbus.ObjectPath // active access point
}

// Network reads NetworkManager's state. Secrets are never asked for.
func (s *Settings) Network(ctx context.Context) Network {
	out := Network{Devices: []NetDevice{}, AccessPoints: []AccessPoint{}, Connections: []Connection{}}
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return out
	}
	defer func() { _ = bus.Close() }()
	props, err := bus.GetAll(ctx, nm, nmPath, nm)
	if err != nil {
		return out
	}
	out.Available = true
	out.WifiEnabled, out.WifiHardware = boolean(props, "WirelessEnabled"), boolean(props, "WirelessHardwareEnabled")

	// Active connections: their UUIDs and names by object path.
	activeName := map[dbus.ObjectPath]string{}
	activeUUID := map[string]bool{}
	actives, _ := props["ActiveConnections"].Value().([]dbus.ObjectPath)
	for _, a := range actives {
		if p, err := bus.GetAll(ctx, nm, a, nmActive); err == nil {
			activeName[a] = str(p, "Id")
			activeUUID[str(p, "Uuid")] = true
		}
	}

	devs := s.devices(ctx, bus, activeName)
	for _, d := range devs {
		out.Devices = append(out.Devices, d.info)
	}

	// Saved connections.
	var conns []dbus.ObjectPath
	_ = bus.Call(ctx, nm, nmSettingsPath, nm+".Settings.ListConnections", []any{&conns})
	savedSSID := map[string]bool{}
	for _, c := range conns {
		var set map[string]map[string]dbus.Variant
		if err := bus.Call(ctx, nm, c, nmConn+".GetSettings", []any{&set}); err != nil {
			continue
		}
		cn := Connection{UUID: str(set["connection"], "uuid"), Name: str(set["connection"], "id"),
			Type: friendlyType(str(set["connection"], "type")), Autoconnect: true}
		if v, ok := set["connection"]["autoconnect"].Value().(bool); ok {
			cn.Autoconnect = v
		}
		if ssid, ok := set["802-11-wireless"]["ssid"].Value().([]byte); ok {
			cn.SSID = ssidText(ssid)
			savedSSID[cn.SSID] = true
		}
		if cn.UUID == "" || !userTypes[cn.Type] {
			continue // bridges, tunnels, loopback: made by Docker, VPN clients, NM itself
		}
		cn.Active = activeUUID[cn.UUID]
		out.Connections = append(out.Connections, cn)
	}
	sort.Slice(out.Connections, func(i, j int) bool {
		a, b := out.Connections[i], out.Connections[j]
		if a.Active != b.Active {
			return a.Active
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	// Access points, the strongest per name.
	best := map[string]AccessPoint{}
	for _, d := range devs {
		for _, ap := range d.aps {
			p, err := bus.GetAll(ctx, nm, ap, nmAP)
			if err != nil {
				continue
			}
			raw, _ := p["Ssid"].Value().([]byte)
			ssid := ssidText(raw)
			if ssid == "" {
				continue // hidden network
			}
			strength, _ := p["Strength"].Value().(byte)
			a := AccessPoint{SSID: ssid, Device: d.info.Name, Strength: int(strength), Security: security(p),
				Active: ap == d.ap, Saved: savedSSID[ssid]}
			if old, ok := best[ssid]; !ok || a.Active || (!old.Active && a.Strength > old.Strength) {
				best[ssid] = a
			}
		}
	}
	for _, a := range best {
		out.AccessPoints = append(out.AccessPoints, a)
	}
	sort.Slice(out.AccessPoints, func(i, j int) bool {
		a, b := out.AccessPoints[i], out.AccessPoints[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.Strength != b.Strength {
			return a.Strength > b.Strength
		}
		return a.SSID < b.SSID
	})
	return out
}

func (s *Settings) devices(ctx context.Context, bus Bus, activeName map[dbus.ObjectPath]string) []nmDev {
	var paths []dbus.ObjectPath
	if err := bus.Call(ctx, nm, nmPath, nm+".GetDevices", []any{&paths}); err != nil {
		return nil
	}
	var out []nmDev
	for _, p := range paths {
		props, err := bus.GetAll(ctx, nm, p, nmDevice)
		if err != nil {
			continue
		}
		typ, _ := props["DeviceType"].Value().(uint32)
		if typ != nmDeviceWifi && typ != nmDeviceEthernet {
			continue // bridges, loopback, tunnels: the Network page shows those
		}
		state, _ := props["State"].Value().(uint32)
		ac, _ := props["ActiveConnection"].Value().(dbus.ObjectPath)
		d := nmDev{obj: p, typ: typ, info: NetDevice{Name: str(props, "Interface"), Type: "ethernet",
			State: deviceStates[state], Connection: activeName[ac]}}
		if typ == nmDeviceWifi {
			d.info.Type = "wifi"
			if w, err := bus.GetAll(ctx, nm, p, nmWireless); err == nil {
				d.aps, _ = w["AccessPoints"].Value().([]dbus.ObjectPath)
				d.ap, _ = w["ActiveAccessPoint"].Value().(dbus.ObjectPath)
			}
		}
		out = append(out, d)
	}
	return out
}

// userTypes are the connection types a person sets up and switches.
var userTypes = map[string]bool{"wifi": true, "ethernet": true, "vpn": true, "wireguard": true,
	"gsm": true, "cdma": true, "bluetooth": true, "pppoe": true}

func friendlyType(t string) string {
	switch t {
	case "802-11-wireless":
		return "wifi"
	case "802-3-ethernet":
		return "ethernet"
	case "":
		return ""
	}
	return t // vpn, wireguard, bridge, tun, gsm...
}

// ssidText is an SSID as text; invalid UTF-8 and control characters are
// replaced, so a hostile network name is only ever shown, never parsed.
func ssidText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, s)
}

func security(p map[string]dbus.Variant) string {
	flags, _ := p["Flags"].Value().(uint32)
	wpa, _ := p["WpaFlags"].Value().(uint32)
	rsn, _ := p["RsnFlags"].Value().(uint32)
	switch km := wpa | rsn; {
	case km&nmKeyMgmt8021X != 0:
		return "enterprise"
	case km&(nmKeyMgmtPSK|nmKeyMgmtSAE) != 0, flags&nmApPrivacy != 0 && km&nmKeyMgmtOWE == 0:
		return "psk"
	}
	return "open"
}

// uuidRe is a connection UUID.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ifaceRe is a network interface name.
var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

// ValidSSID reports whether s can name a network to join: 1 to 32 bytes
// of UTF-8 without control characters.
func ValidSSID(s string) bool {
	return s != "" && len(s) <= 32 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func (s *Settings) registerNetwork(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "wifi", Name: "radio", Risk: actions.Reversible,
		Validate: onOff,
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return one("turn Wi-Fi "+r.Target, "wifi", r.Target), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.nmCall(ctx, p, nmPath, "org.freedesktop.DBus.Properties.Set", nm, "WirelessEnabled", dbus.MakeVariant(r.Target == "on"))
		}})

	reg.Register(actions.Action{Kind: "wifi", Name: "scan", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if !ifaceRe.MatchString(r.Target) || len(r.Params) != 0 {
				return errors.New("target must be a Wi-Fi device and params empty")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			p := one("scan for Wi-Fi networks", r.Target, r.Target)
			_, err := s.wifiDevice(ctx, r.Target)
			return p, err
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			dev, err := s.wifiDevice(ctx, r.Target)
			if err != nil {
				return actions.Result{}, err
			}
			return s.nmCall(ctx, p, dev, nmWireless+".RequestScan", map[string]dbus.Variant{})
		}})

	// join connects to a network in range that no saved connection names.
	// Hearth never sees a password: for a secured network NetworkManager
	// asks the desktop's secret agent, which shows its own dialog.
	reg.Register(actions.Action{Kind: "wifi", Name: "join", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if !ValidSSID(r.Target) || len(r.Params) != 1 || !ifaceRe.MatchString(r.Params["device"]) {
				return errors.New(`target must be a network name and params {"device": "<Wi-Fi device>"}`)
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			n := s.Network(ctx)
			for _, a := range n.AccessPoints {
				if a.SSID != r.Target || a.Device != r.Params["device"] {
					continue
				}
				p := one("join "+a.SSID, a.SSID, a.SSID)
				p.Items[0].Detail = a.Security
				if a.Security == "enterprise" {
					p.Items, p.Blocked = []actions.Item{}, []actions.Blocked{{Item: actions.Item{ID: a.SSID, Name: a.SSID}, Reason: "enterprise"}}
				}
				return p, nil
			}
			return actions.Plan{}, actions.Errorf(http.StatusNotFound, "%s is not in range", r.Target)
		},
		Run: s.join})

	reg.Register(actions.Action{Kind: "network", Name: "connect", Risk: actions.Reversible,
		Validate: uuidTarget,
		Plan:     s.planConnection("connect"),
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			path, _ := p.State.(dbus.ObjectPath)
			return s.nmCall(ctx, p, nmPath, nm+".ActivateConnection", path, dbus.ObjectPath("/"), dbus.ObjectPath("/"))
		}})

	reg.Register(actions.Action{Kind: "network", Name: "disconnect", Risk: actions.Reversible,
		Validate: uuidTarget,
		Plan:     s.planConnection("disconnect"),
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			path, _ := p.State.(dbus.ObjectPath)
			return s.nmCall(ctx, p, nmPath, nm+".DeactivateConnection", path)
		}})
}

func uuidTarget(r actions.Request) error {
	if !uuidRe.MatchString(r.Target) || len(r.Params) != 0 {
		return errors.New("target must be a connection UUID and params empty")
	}
	return nil
}

// planConnection finds the saved connection (connect) or its active
// connection (disconnect); State is the object path to call with.
func (s *Settings) planConnection(verb string) func(context.Context, actions.Request) (actions.Plan, error) {
	return func(ctx context.Context, r actions.Request) (actions.Plan, error) {
		bus, err := s.cfg.System(ctx)
		if err != nil {
			return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
		}
		defer func() { _ = bus.Close() }()
		var path dbus.ObjectPath
		var name string
		if verb == "connect" {
			if err := bus.Call(ctx, nm, nmSettingsPath, nm+".Settings.GetConnectionByUuid", []any{&path}, r.Target); err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no saved connection %s", r.Target)
			}
			var set map[string]map[string]dbus.Variant
			if err := bus.Call(ctx, nm, path, nmConn+".GetSettings", []any{&set}); err == nil {
				name = str(set["connection"], "id")
			}
		} else {
			props, err := bus.GetAll(ctx, nm, nmPath, nm)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "NetworkManager is not available: %v", err)
			}
			actives, _ := props["ActiveConnections"].Value().([]dbus.ObjectPath)
			for _, a := range actives {
				if p, err := bus.GetAll(ctx, nm, a, nmActive); err == nil && str(p, "Uuid") == r.Target {
					path, name = a, str(p, "Id")
					break
				}
			}
			if path == "" {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "connection %s is not active", r.Target)
			}
		}
		p := one(verb+" "+name, r.Target, name)
		p.State = path
		return p, nil
	}
}

// wifiDevice is the object path of the Wi-Fi device named iface.
func (s *Settings) wifiDevice(ctx context.Context, iface string) (dbus.ObjectPath, error) {
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return "", actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	for _, d := range s.devices(ctx, bus, nil) {
		if d.info.Name == iface && d.typ == nmDeviceWifi {
			return d.obj, nil
		}
	}
	return "", actions.Errorf(http.StatusNotFound, "no Wi-Fi device %s", iface)
}

// join adds a connection for the network and activates it on the device;
// NetworkManager picks the access point and asks the secret agent for a
// key if the network needs one.
func (s *Settings) join(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
	if len(p.Items) == 0 {
		return actions.Result{Failed: []actions.Blocked{}}, nil
	}
	dev, err := s.wifiDevice(ctx, r.Params["device"])
	if err != nil {
		return actions.Result{}, err
	}
	set := map[string]map[string]dbus.Variant{
		"connection":      {"id": dbus.MakeVariant(r.Target), "type": dbus.MakeVariant("802-11-wireless")},
		"802-11-wireless": {"ssid": dbus.MakeVariant([]byte(r.Target))},
	}
	switch p.Items[0].Detail {
	case "psk":
		// SAE networks also take wpa-psk: NetworkManager upgrades it.
		set["802-11-wireless-security"] = map[string]dbus.Variant{"key-mgmt": dbus.MakeVariant("wpa-psk")}
	}
	return s.nmCallOut(ctx, p, []any{new(dbus.ObjectPath), new(dbus.ObjectPath)}, nmPath, nm+".AddAndActivateConnection", set, dev, dbus.ObjectPath("/"))
}

func (s *Settings) nmCall(ctx context.Context, p actions.Plan, path dbus.ObjectPath, method string, args ...any) (actions.Result, error) {
	return s.nmCallOut(ctx, p, nil, path, method, args...)
}

func (s *Settings) nmCallOut(ctx context.Context, p actions.Plan, out []any, path dbus.ObjectPath, method string, args ...any) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return res, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the system bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	if err := bus.CallInteractive(ctx, nm, path, method, out, args...); err != nil {
		res.Failed = append(res.Failed, Failure(p.Items[0], err))
		return res, nil
	}
	res.Done = 1
	return res, nil
}

// ConnectionActive reports whether the NetworkManager connection uuid is
// active now (a mode's connection items, ROADMAP 10.5).
func (s *Settings) ConnectionActive(ctx context.Context, uuid string) (bool, error) {
	if !uuidRe.MatchString(uuid) {
		return false, errors.New("settings: not a connection UUID")
	}
	active, err := s.ActiveConnections(ctx)
	return active[uuid], err
}

// ActiveConnections are the UUIDs of the active NetworkManager connections
// (a mode's Wi-Fi trigger, ROADMAP 10.6).
func (s *Settings) ActiveConnections(ctx context.Context) (map[string]bool, error) {
	bus, err := s.cfg.System(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bus.Close() }()
	props, err := bus.GetAll(ctx, nm, nmPath, nm)
	if err != nil {
		return nil, err
	}
	actives, _ := props["ActiveConnections"].Value().([]dbus.ObjectPath)
	out := map[string]bool{}
	for _, a := range actives {
		if p, err := bus.GetAll(ctx, nm, a, nmActive); err == nil {
			if u := str(p, "Uuid"); uuidRe.MatchString(u) {
				out[u] = true
			}
		}
	}
	return out, nil
}
