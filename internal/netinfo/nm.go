package netinfo

import (
	"context"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	nmDest = "org.freedesktop.NetworkManager"
	nmPath = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmIfc  = "org.freedesktop.NetworkManager"
)

// NM is NetworkManager's state.
type NM struct {
	Running      bool     `json:"running"`
	State        string   `json:"state,omitempty"`        // connected-global, disconnected, ...
	Connectivity string   `json:"connectivity,omitempty"` // full, limited, portal, none, unknown
	Connections  []NMConn `json:"connections"`
}

// NMConn is one active connection.
type NMConn struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"` // NM's type: 802-11-wireless, 802-3-ethernet, vpn, wireguard, bridge...
	VPN     bool     `json:"vpn,omitempty"`
	Default bool     `json:"default,omitempty"` // carries the default route
	State   string   `json:"state"`
	Devices []string `json:"devices"`
	SSID    string   `json:"ssid,omitempty"`
	Signal  *int     `json:"signal,omitempty"` // Wi-Fi strength, percent
}

// maxNMConns bounds the active connections read (Docker adds one per
// bridge and veth).
const maxNMConns = 64

var nmStates = map[uint32]string{10: "asleep", 20: "disconnected", 30: "disconnecting", 40: "connecting",
	50: "connected-local", 60: "connected-site", 70: "connected-global"}

var nmConnectivity = map[uint32]string{1: "none", 2: "portal", 3: "limited", 4: "full"}

var nmConnStates = map[uint32]string{1: "activating", 2: "activated", 3: "deactivating", 4: "deactivated"}

// ReadNM reads NetworkManager's state over the system bus. Running is
// false when NetworkManager is not there.
func ReadNM(ctx context.Context, bus dbusx.Bus) NM {
	out := NM{Connections: []NMConn{}}
	props, err := bus.GetAll(ctx, nmDest, nmPath, nmIfc)
	if err != nil {
		return out
	}
	out.Running = true
	out.State = enumName(nmStates, props["State"])
	out.Connectivity = enumName(nmConnectivity, props["Connectivity"])
	paths, _ := props["ActiveConnections"].Value().([]dbus.ObjectPath)
	for _, p := range paths {
		if len(out.Connections) >= maxNMConns {
			break
		}
		if c, ok := readConn(ctx, bus, p); ok {
			out.Connections = append(out.Connections, c)
		}
	}
	// The default route's connection and VPNs first; Docker's bridges and
	// veths last.
	sort.SliceStable(out.Connections, func(i, j int) bool { return connRank(out.Connections[i]) < connRank(out.Connections[j]) })
	return out
}

func connRank(c NMConn) int {
	switch {
	case c.Default:
		return 0
	case c.VPN || c.Type == "vpn" || c.Type == "wireguard":
		return 1
	case c.Type == "802-11-wireless" || c.Type == "802-3-ethernet" || c.Type == "gsm":
		return 2
	case c.Type == "loopback" || c.Type == "bridge" || c.Type == "veth" || c.Type == "tun":
		return 4
	default:
		return 3
	}
}

func readConn(ctx context.Context, bus dbusx.Bus, p dbus.ObjectPath) (NMConn, bool) {
	props, err := bus.GetAll(ctx, nmDest, p, nmIfc+".Connection.Active")
	if err != nil {
		return NMConn{}, false
	}
	c := NMConn{ID: str(props["Id"], 200), Type: str(props["Type"], 64), State: enumName(nmConnStates, props["State"]),
		Devices: []string{}}
	c.VPN, _ = props["Vpn"].Value().(bool)
	d4, _ := props["Default"].Value().(bool)
	d6, _ := props["Default6"].Value().(bool)
	c.Default = d4 || d6
	devs, _ := props["Devices"].Value().([]dbus.ObjectPath)
	for _, d := range devs {
		dp, err := bus.GetAll(ctx, nmDest, d, nmIfc+".Device")
		if err != nil {
			continue
		}
		c.Devices = append(c.Devices, str(dp["Interface"], 64))
		if t, _ := dp["DeviceType"].Value().(uint32); t == 2 && c.SSID == "" { // NM_DEVICE_TYPE_WIFI
			c.SSID, c.Signal = accessPoint(ctx, bus, d)
		}
	}
	return c, true
}

// accessPoint reads a Wi-Fi device's current network name and signal.
func accessPoint(ctx context.Context, bus dbusx.Bus, dev dbus.ObjectPath) (string, *int) {
	wp, err := bus.GetAll(ctx, nmDest, dev, nmIfc+".Device.Wireless")
	if err != nil {
		return "", nil
	}
	ap, _ := wp["ActiveAccessPoint"].Value().(dbus.ObjectPath)
	if ap == "" || ap == "/" {
		return "", nil
	}
	props, err := bus.GetAll(ctx, nmDest, ap, nmIfc+".AccessPoint")
	if err != nil {
		return "", nil
	}
	ssid, _ := props["Ssid"].Value().([]byte)
	var sig *int
	if s, ok := props["Strength"].Value().(byte); ok {
		v := int(s)
		sig = &v
	}
	return clip(strings.ToValidUTF8(string(ssid), "�"), 64), sig
}

func enumName(m map[uint32]string, v dbus.Variant) string {
	n, ok := v.Value().(uint32)
	if !ok {
		return ""
	}
	if s, ok := m[n]; ok {
		return s
	}
	return "unknown"
}

func str(v dbus.Variant, max int) string {
	s, _ := v.Value().(string)
	return clip(strings.ToValidUTF8(s, "�"), max)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Firewall is one firewall front end.
type Firewall struct {
	Name      string `json:"name"` // ufw, firewalld, nftables
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
}

// FirewallConfig says where to look (tests point it elsewhere).
type FirewallConfig struct {
	Root string // prefix for files, default ""
}

// ReadFirewalls reports the usual firewall front ends: whether each is
// installed and active. ufw is active when ufw.conf enables it and its
// unit ran; firewalld and nftables when their unit is active (the rules
// themselves need root to read).
func ReadFirewalls(ctx context.Context, bus dbusx.Bus, c FirewallConfig) []Firewall {
	exists := func(p string) bool { _, err := os.Stat(c.Root + p); return err == nil }
	active := func(unit string) bool { return UnitActive(ctx, bus, unit) }
	ufw := Firewall{Name: "ufw", Installed: exists("/usr/sbin/ufw")}
	ufw.Active = ufw.Installed && ufwEnabled(c.Root+"/etc/ufw/ufw.conf") && active("ufw.service")
	fwd := Firewall{Name: "firewalld", Installed: exists("/usr/sbin/firewalld")}
	fwd.Active = fwd.Installed && active("firewalld.service")
	nft := Firewall{Name: "nftables", Installed: exists("/usr/sbin/nft")}
	nft.Active = nft.Installed && active("nftables.service")
	return []Firewall{ufw, fwd, nft}
}

// UnitActive reports whether systemd unit is loaded and active on bus.
func UnitActive(ctx context.Context, bus dbusx.Bus, unit string) bool {
	var path dbus.ObjectPath
	if bus == nil || bus.Call(ctx, "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager.GetUnit", []any{&path}, unit) != nil {
		return false // not loaded
	}
	props, err := bus.GetAll(ctx, "org.freedesktop.systemd1", path, "org.freedesktop.systemd1.Unit")
	if err != nil {
		return false
	}
	s, _ := props["ActiveState"].Value().(string)
	return s == "active"
}

// ufwEnabled reads ENABLED=yes from ufw.conf.
func ufwEnabled(path string) bool {
	on := false
	eachLine(path, func(f []string) {
		if len(f) == 1 {
			k, v, _ := strings.Cut(f[0], "=")
			if k == "ENABLED" {
				on = strings.Trim(v, `"'`) == "yes"
			}
		}
	})
	return on
}
