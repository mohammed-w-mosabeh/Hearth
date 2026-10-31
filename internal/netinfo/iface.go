package netinfo

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Interface is one network interface.
type Interface struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"` // loopback, wifi, ethernet, bridge, virtual
	MAC   string   `json:"mac,omitempty"`
	MTU   int      `json:"mtu"`
	Up    bool     `json:"up"`
	Addrs []string `json:"addrs"` // CIDR
}

// Route is a default route.
type Route struct {
	Gateway   string `json:"gateway"`
	Interface string `json:"interface"`
}

// Addressing is the interfaces, default routes and DNS servers.
type Addressing struct {
	Interfaces []Interface `json:"interfaces"`
	Gateways   []Route     `json:"gateways"`
	DNS        []string    `json:"dns"`
	// DNSStub is set when resolv.conf points at systemd-resolved's stub;
	// DNS is then resolved's upstream servers.
	DNSStub bool `json:"dns_stub,omitempty"`
}

// Paths are the files Addressing reads (tests point them elsewhere).
type Paths struct {
	Proc, Sys  string // default /proc, /sys
	ResolvConf string // default /etc/resolv.conf
	// ResolvedConf lists resolved's upstream servers; default
	// /run/systemd/resolve/resolv.conf.
	ResolvedConf string
}

func (p Paths) withDefaults() Paths {
	if p.Proc == "" {
		p.Proc = "/proc"
	}
	if p.Sys == "" {
		p.Sys = "/sys"
	}
	if p.ResolvConf == "" {
		p.ResolvConf = "/etc/resolv.conf"
	}
	if p.ResolvedConf == "" {
		p.ResolvedConf = "/run/systemd/resolve/resolv.conf"
	}
	return p
}

// ReadAddressing reads the interfaces (over netlink), default routes and
// DNS servers.
func ReadAddressing(p Paths) Addressing {
	p = p.withDefaults()
	out := Addressing{Interfaces: []Interface{}, Gateways: []Route{}, DNS: []string{}}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		x := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), MTU: i.MTU, Up: i.Flags&net.FlagUp != 0,
			Kind: kindOf(p.Sys, i), Addrs: []string{}}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			x.Addrs = append(x.Addrs, a.String())
		}
		out.Interfaces = append(out.Interfaces, x)
	}
	out.Gateways = append(readRoutes4(filepath.Join(p.Proc, "net/route")), readRoutes6(filepath.Join(p.Proc, "net/ipv6_route"))...)
	out.DNS = nameservers(p.ResolvConf)
	if len(out.DNS) > 0 && allStub(out.DNS) {
		if up := nameservers(p.ResolvedConf); len(up) > 0 {
			out.DNS, out.DNSStub = up, true
		}
	}
	return out
}

// kindOf classifies an interface from sysfs.
func kindOf(sys string, i net.Interface) string {
	dir := filepath.Join(sys, "class/net", i.Name)
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(dir, rel)); return err == nil }
	switch {
	case i.Flags&net.FlagLoopback != 0:
		return "loopback"
	case exists("wireless") || exists("phy80211"):
		return "wifi"
	case exists("bridge"):
		return "bridge"
	case exists("device"):
		return "ethernet"
	default:
		return "virtual" // veth, tun, wireguard, docker0's peers
	}
}

// readRoutes4 reads the default routes from /proc/net/route (hex, host
// byte order).
func readRoutes4(path string) []Route {
	var out []Route
	eachLine(path, func(f []string) {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			return
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			return
		}
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], binary.NativeEndian.Uint32(b))
		if gw := netip.AddrFrom4(a); !gw.IsUnspecified() {
			out = append(out, Route{Gateway: gw.String(), Interface: f[0]})
		}
	})
	return out
}

// readRoutes6 reads the default routes from /proc/net/ipv6_route.
func readRoutes6(path string) []Route {
	var out []Route
	eachLine(path, func(f []string) {
		// dest destlen src srclen nexthop metric refcnt use flags iface
		if len(f) < 10 || f[1] != "00" || strings.Trim(f[0], "0") != "" || f[9] == "lo" {
			return
		}
		b, err := hex.DecodeString(f[4])
		if err != nil || len(b) != 16 {
			return
		}
		if gw := netip.AddrFrom16([16]byte(b)); !gw.IsUnspecified() {
			out = append(out, Route{Gateway: gw.String(), Interface: f[9]})
		}
	})
	return out
}

func eachLine(path string, fn func([]string)) {
	f, err := os.Open(path) // #nosec G304 -- fixed /proc paths
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fn(strings.Fields(sc.Text()))
	}
}

// nameservers reads resolv.conf's nameserver lines (at most 8).
func nameservers(path string) []string {
	var out []string
	eachLine(path, func(f []string) {
		if len(f) >= 2 && f[0] == "nameserver" && len(out) < 8 {
			if a, err := netip.ParseAddr(f[1]); err == nil {
				out = append(out, a.String())
			}
		}
	})
	return out
}

// allStub reports whether every server is resolved's local stub.
func allStub(dns []string) bool {
	for _, d := range dns {
		if d != "127.0.0.53" && d != "127.0.0.54" {
			return false
		}
	}
	return true
}
