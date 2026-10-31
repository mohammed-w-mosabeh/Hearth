package netinfo

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

// diagMsg builds an inet_diag_msg with a cgroup id and, if sent > 0, a
// tcp_info attribute.
func diagMsg(family uint8, state uint8, src, dst netip.AddrPort, uid, inode uint32, cg uint64, sent, recv uint64) []byte {
	ne := binary.NativeEndian
	b := make([]byte, sizeofMsg)
	b[0], b[1] = family, state
	id := b[4:]
	binary.BigEndian.PutUint16(id[0:], src.Port())
	binary.BigEndian.PutUint16(id[2:], dst.Port())
	s, d := src.Addr().As16(), dst.Addr().As16()
	if family == syscall.AF_INET {
		s4, d4 := src.Addr().As4(), dst.Addr().As4()
		copy(id[4:], s4[:])
		copy(id[20:], d4[:])
	} else {
		copy(id[4:], s[:])
		copy(id[20:], d[:])
	}
	rest := b[4+sizeofSockID:]
	ne.PutUint32(rest[12:], uid)
	ne.PutUint32(rest[16:], inode)
	attr := func(typ uint16, v []byte) {
		a := make([]byte, (4+len(v)+3)&^3)
		ne.PutUint16(a[0:], uint16(4+len(v))) // #nosec G115 -- small test attributes
		ne.PutUint16(a[2:], typ)
		copy(a[4:], v)
		b = append(b, a...)
	}
	if sent > 0 {
		info := make([]byte, 232)
		ne.PutUint64(info[120:], sent)
		ne.PutUint64(info[128:], recv)
		attr(inetDiagInfo, info)
	}
	v := make([]byte, 8)
	ne.PutUint64(v, cg)
	attr(inetDiagCgroupID, v)
	return b
}

func TestParseMsg(t *testing.T) {
	src, dst := netip.MustParseAddrPort("192.168.1.5:443"), netip.MustParseAddrPort("10.0.0.9:51000")
	s, ok := parseMsg(diagMsg(syscall.AF_INET, stateEstablished, src, dst, 1000, 4242, 77, 1234, 5678), syscall.IPPROTO_TCP)
	if !ok || s.Local != src || s.Remote != dst || s.UID != 1000 || s.Inode != 4242 || s.Cgroup != 77 ||
		s.Sent != 1234 || s.Received != 5678 || s.Proto != "tcp" || s.State != stateEstablished {
		t.Fatalf("v4: %+v", s)
	}
	l := netip.MustParseAddrPort("[::]:53")
	s, ok = parseMsg(diagMsg(syscall.AF_INET6, stateClose, l, netip.AddrPortFrom(netip.IPv6Unspecified(), 0), 0, 9, 5, 0, 0), syscall.IPPROTO_UDP)
	if !ok || s.Local != l || s.Proto != "udp" || s.Cgroup != 5 {
		t.Fatalf("v6: %+v", s)
	}
	mapped := netip.MustParseAddrPort("[::ffff:127.0.0.1]:8080")
	s, _ = parseMsg(diagMsg(syscall.AF_INET6, stateListen, mapped, netip.AddrPort{}, 0, 1, 0, 0, 0), syscall.IPPROTO_TCP)
	if s.Local.Addr().String() != "127.0.0.1" {
		t.Errorf("v4-mapped not unmapped: %v", s.Local)
	}
	// Short and malformed messages.
	if _, ok := parseMsg(make([]byte, sizeofMsg-1), syscall.IPPROTO_TCP); ok {
		t.Error("short message accepted")
	}
	bad := diagMsg(syscall.AF_INET, stateListen, src, dst, 0, 1, 0, 0, 0)
	binary.NativeEndian.PutUint16(bad[sizeofMsg:], 200) // attribute longer than the message
	if s, ok := parseMsg(bad, syscall.IPPROTO_TCP); !ok || s.Cgroup != 0 {
		t.Errorf("bad attribute: %+v %v", s, ok)
	}
}

func TestRequest(t *testing.T) {
	b := request(syscall.AF_INET6, syscall.IPPROTO_TCP, 1<<stateListen, 7)
	ne := binary.NativeEndian
	if len(b) != 72 || ne.Uint32(b) != 72 || ne.Uint16(b[4:]) != 20 || ne.Uint32(b[8:]) != 7 {
		t.Fatalf("header % x", b[:16])
	}
	r := b[16:]
	if r[0] != syscall.AF_INET6 || r[1] != syscall.IPPROTO_TCP || r[2] != 1<<(inetDiagInfo-1) || ne.Uint32(r[4:]) != 1<<stateListen {
		t.Fatalf("req % x", r[:8])
	}
	if request(syscall.AF_INET, syscall.IPPROTO_UDP, 0, 1)[16+2] != 0 {
		t.Error("tcp_info asked for UDP")
	}
}

func TestParseStat(t *testing.T) {
	name, start := parseStat([]byte("123 (my (odd) prog) S 1 123 123 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000 10"))
	if name != "my (odd) prog" || start != 98765 {
		t.Fatalf("%q %d", name, start)
	}
	if n, s := parseStat([]byte("garbage")); n != "" || s != 0 {
		t.Fatal("garbage parsed")
	}
}

// fakeSystem lays out a /proc and cgroup tree: the user's process 200
// (holds socket 11) and a root service whose fd directory is unreadable.
func fakeSystem(t *testing.T) (*Resolver, map[string]uint64) {
	t.Helper()
	dir := t.TempDir()
	proc, cg := filepath.Join(dir, "proc"), filepath.Join(dir, "cgroup")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stat := func(pid, comm string) string {
		return pid + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5000 0 0"
	}
	userApp := "/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-org.example.Chat-200.scope"
	write(filepath.Join(proc, "200/stat"), stat("200", "chat"))
	write(filepath.Join(proc, "200/cgroup"), "0::"+userApp+"\n")
	if err := os.MkdirAll(filepath.Join(proc, "200/fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	for fd, target := range map[string]string{"3": "socket:[11]", "4": "/dev/null", "5": "socket:[99]"} {
		if err := os.Symlink(target, filepath.Join(proc, "200/fd", fd)); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(proc, "300/stat"), stat("300", "sshd"))
	write(filepath.Join(proc, "300/cgroup"), "0::/system.slice/ssh.service\n")
	write(filepath.Join(proc, "400/stat"), stat("400", "nginx"))
	write(filepath.Join(proc, "400/cgroup"), "0::/system.slice/docker-0123456789abcdef0123.scope\n")
	write(filepath.Join(cg, "system.slice/ssh.service/cgroup.procs"), "310\n300\n")
	write(filepath.Join(cg, "system.slice/docker-0123456789abcdef0123.scope/cgroup.procs"), "400\n")
	write(filepath.Join(cg, userApp[1:], "cgroup.procs"), "200\n")
	ids := map[string]uint64{}
	for name, rel := range map[string]string{"ssh": "system.slice/ssh.service", "docker": "system.slice/docker-0123456789abcdef0123.scope", "app": userApp[1:]} {
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join(cg, rel), &st); err != nil {
			t.Fatal(err)
		}
		ids[name] = st.Ino
	}
	return &Resolver{ProcRoot: proc, CgroupRoot: cg, AppName: func(id string) (string, bool) { return "Example Chat", id == "org.example.Chat" }}, ids
}

func TestRead(t *testing.T) {
	r, cg := fakeSystem(t)
	ap := netip.MustParseAddrPort
	dump := func(proto uint8, states uint32) ([]Socket, error) {
		if proto == syscall.IPPROTO_UDP {
			return []Socket{
				{Proto: "udp", State: stateClose, Local: ap("0.0.0.0:5353"), UID: 1000, Inode: 11, Cgroup: cg["app"]},
				{Proto: "udp", State: stateClose, Local: ap("0.0.0.0:0"), UID: 1000, Inode: 12}, // unbound
			}, nil
		}
		return []Socket{
			{Proto: "tcp", State: stateListen, Local: ap("[::]:22"), UID: 0, Inode: 21, Cgroup: cg["ssh"]},
			{Proto: "tcp", State: stateListen, Local: ap("127.0.0.1:8080"), UID: 0, Inode: 22, Cgroup: cg["docker"]},
			{Proto: "tcp", State: stateListen, Local: ap("192.168.1.5:9000"), UID: 999, Inode: 23, Cgroup: 123456789}, // unknown cgroup
			{Proto: "tcp", State: stateEstablished, Local: ap("192.168.1.5:22"), Remote: ap("192.168.1.9:50000"),
				UID: 0, Inode: 24, Cgroup: cg["ssh"], Sent: 10, Received: 20},
			{Proto: "tcp", State: 6, Local: ap("192.168.1.5:22"), Remote: ap("192.168.1.9:50001")}, // TIME_WAIT
		}, nil
	}
	s, err := r.Read(dump, map[int]string{0: "root", 1000: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Listeners) != 4 || len(s.Connections) != 1 {
		t.Fatalf("listeners %d connections %d", len(s.Listeners), len(s.Connections))
	}
	by := map[uint16]Endpoint{}
	for _, e := range s.Listeners {
		by[e.Port] = e
	}
	ssh := by[22]
	if ssh.Scope != ScopeAll || ssh.User != "root" || ssh.Owner == nil || ssh.Owner.Group != "unit:ssh.service" ||
		ssh.Owner.PID != 300 || ssh.Owner.Name != "sshd" || ssh.Owner.Exact || ssh.Owner.Start != 5000 {
		t.Errorf("ssh: %+v %+v", ssh, ssh.Owner)
	}
	dock := by[8080]
	if dock.Scope != ScopeLoopback || dock.Owner == nil || dock.Owner.Container != "0123456789ab" || dock.Owner.Kind != "container" {
		t.Errorf("container: %+v %+v", dock, dock.Owner)
	}
	mdns := by[5353]
	if mdns.User != "me" || mdns.Owner == nil || !mdns.Owner.Exact || mdns.Owner.PID != 200 ||
		mdns.Owner.Group != "app:org.example.Chat" || mdns.Owner.GroupName != "Example Chat" {
		t.Errorf("own app: %+v %+v", mdns, mdns.Owner)
	}
	if u := by[9000]; u.Scope != ScopeAddress || u.Owner != nil || u.User != "999" {
		t.Errorf("unknown: %+v %+v", u, u.Owner)
	}
	c := s.Connections[0]
	if c.Remote != "192.168.1.9:50000" || c.Sent != 10 || c.Received != 20 || c.Owner == nil || c.Owner.Unit != "ssh.service" {
		t.Errorf("connection: %+v", c)
	}
	if _, err := r.Read(func(uint8, uint32) ([]Socket, error) { return nil, errors.New("no") }, nil); err == nil {
		t.Error("dump error not returned")
	}
}

func TestAddressingFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var gw [4]byte
	binary.NativeEndian.PutUint32(gw[:], binary.BigEndian.Uint32([]byte{192, 0, 2, 1}))
	hexGW := fmt.Sprintf("%02X%02X%02X%02X", gw[0], gw[1], gw[2], gw[3])
	write("proc/net/route", "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\n"+
		"wlp3s0\t00000000\t"+hexGW+"\t0003\t0\t0\t600\t00000000\n"+
		"wlp3s0\t000200C0\t00000000\t0001\t0\t0\t600\t00FFFFFF\n")
	write("proc/net/ipv6_route",
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000001 00000000 00000003 wlp3s0\n"+
			"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n")
	r4 := readRoutes4(filepath.Join(dir, "proc/net/route"))
	if len(r4) != 1 || r4[0] != (Route{Gateway: "192.0.2.1", Interface: "wlp3s0"}) {
		t.Errorf("v4 routes %+v", r4)
	}
	r6 := readRoutes6(filepath.Join(dir, "proc/net/ipv6_route"))
	if len(r6) != 1 || r6[0] != (Route{Gateway: "fe80::1", Interface: "wlp3s0"}) {
		t.Errorf("v6 routes %+v", r6)
	}
	stub := write("resolv.conf", "# generated\nnameserver 127.0.0.53\noptions edns0\nsearch lan\n")
	up := write("resolved.conf", "nameserver 1.1.1.1\nnameserver not-an-ip\nnameserver 2606:4700:4700::1111\n")
	a := ReadAddressing(Paths{Proc: filepath.Join(dir, "proc"), Sys: dir, ResolvConf: stub, ResolvedConf: up})
	if !a.DNSStub || strings.Join(a.DNS, ",") != "1.1.1.1,2606:4700:4700::1111" || len(a.Gateways) != 2 {
		t.Errorf("addressing %+v", a)
	}
	plain := write("plain.conf", "nameserver 9.9.9.9\n")
	if a := ReadAddressing(Paths{Proc: dir, ResolvConf: plain, ResolvedConf: up}); a.DNSStub || a.DNS[0] != "9.9.9.9" {
		t.Errorf("plain %+v", a)
	}
}

type fakeBus struct {
	props map[dbus.ObjectPath]map[string]map[string]dbus.Variant // path -> iface -> props
	units map[string]dbus.ObjectPath
}

func (f *fakeBus) GetAll(_ context.Context, _ string, p dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	if m, ok := f.props[p][iface]; ok {
		return m, nil
	}
	return nil, errors.New("no such object")
}

func (f *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	if method != "org.freedesktop.systemd1.Manager.GetUnit" {
		return errors.New("unexpected " + method)
	}
	p, ok := f.units[args[0].(string)]
	if !ok {
		return errors.New("org.freedesktop.systemd1.NoSuchUnit")
	}
	*out[0].(*dbus.ObjectPath) = p
	return nil
}

func (f *fakeBus) Set(context.Context, string, dbus.ObjectPath, string, string, any) error {
	return errors.New("read only")
}

func (f *fakeBus) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	return nil, errors.New("no")
}

func TestReadNM(t *testing.T) {
	v := dbus.MakeVariant
	ac := func(id, typ string, def, vpn bool, devs ...dbus.ObjectPath) map[string]map[string]dbus.Variant {
		return map[string]map[string]dbus.Variant{nmIfc + ".Connection.Active": {"Id": v(id), "Type": v(typ),
			"Default": v(def), "Default6": v(false), "Vpn": v(vpn), "State": v(uint32(2)), "Devices": v(devs)}}
	}
	bus := &fakeBus{props: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		nmPath: {nmIfc: {"State": v(uint32(70)), "Connectivity": v(uint32(4)),
			"ActiveConnections": v([]dbus.ObjectPath{"/ac/1", "/ac/2", "/ac/3", "/ac/gone"})}},
		"/ac/1":   ac("br-123", "bridge", false, false, "/dev/br"),
		"/ac/2":   ac("Home <b>", "802-11-wireless", true, false, "/dev/wifi"),
		"/ac/3":   ac("Office VPN", "vpn", false, true),
		"/dev/br": {nmIfc + ".Device": {"Interface": v("br-123"), "DeviceType": v(uint32(13))}},
		"/dev/wifi": {nmIfc + ".Device": {"Interface": v("wlp3s0"), "DeviceType": v(uint32(2))},
			nmIfc + ".Device.Wireless": {"ActiveAccessPoint": v(dbus.ObjectPath("/ap/1"))}},
		"/ap/1": {nmIfc + ".AccessPoint": {"Ssid": v([]byte("Home\xff")), "Strength": v(byte(71))}},
	}}
	nm := ReadNM(context.Background(), bus)
	if !nm.Running || nm.State != "connected-global" || nm.Connectivity != "full" || len(nm.Connections) != 3 {
		t.Fatalf("%+v", nm)
	}
	w := nm.Connections[0]
	if w.ID != "Home <b>" || !w.Default || w.SSID != "Home�" || w.Signal == nil || *w.Signal != 71 ||
		len(w.Devices) != 1 || w.Devices[0] != "wlp3s0" || w.State != "activated" {
		t.Errorf("wifi first: %+v", w)
	}
	if nm.Connections[1].ID != "Office VPN" || nm.Connections[2].ID != "br-123" {
		t.Errorf("order: %s, %s", nm.Connections[1].ID, nm.Connections[2].ID)
	}
	if off := ReadNM(context.Background(), &fakeBus{}); off.Running || off.Connections == nil {
		t.Errorf("no NM: %+v", off)
	}
}

func TestReadFirewalls(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"usr/sbin/ufw", "usr/sbin/nft"} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o700)
		_ = os.WriteFile(filepath.Join(root, p), nil, 0o600)
	}
	_ = os.MkdirAll(filepath.Join(root, "etc/ufw"), 0o700)
	conf := filepath.Join(root, "etc/ufw/ufw.conf")
	unit := func(state string) map[string]map[string]dbus.Variant {
		return map[string]map[string]dbus.Variant{"org.freedesktop.systemd1.Unit": {"ActiveState": dbus.MakeVariant(state)}}
	}
	bus := &fakeBus{
		units: map[string]dbus.ObjectPath{"ufw.service": "/u/ufw", "nftables.service": "/u/nft", "firewalld.service": "/u/fwd"},
		props: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/u/ufw": unit("active"), "/u/nft": unit("inactive"), "/u/fwd": unit("active")},
	}
	get := func() map[string]Firewall {
		m := map[string]Firewall{}
		for _, f := range ReadFirewalls(context.Background(), bus, FirewallConfig{Root: root}) {
			m[f.Name] = f
		}
		return m
	}
	_ = os.WriteFile(conf, []byte("# comment\nENABLED=no\n"), 0o600)
	if f := get(); f["ufw"].Active || !f["ufw"].Installed {
		t.Errorf("ufw disabled in conf: %+v", f["ufw"])
	}
	_ = os.WriteFile(conf, []byte("ENABLED=yes\nLOGLEVEL=low\n"), 0o600)
	f := get()
	if !f["ufw"].Active || f["nftables"].Active || !f["nftables"].Installed || f["firewalld"].Installed || f["firewalld"].Active {
		t.Errorf("%+v", f)
	}
	if f := ReadFirewalls(context.Background(), nil, FirewallConfig{Root: root}); f[0].Active {
		t.Error("active without a bus")
	}
}

func TestScope(t *testing.T) {
	for addr, want := range map[string]string{"0.0.0.0": ScopeAll, "::": ScopeAll, "127.0.0.53": ScopeLoopback,
		"::1": ScopeLoopback, "192.168.1.5": ScopeAddress, "fe80::1": ScopeAddress} {
		if got := scopeOf(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %s", addr, got)
		}
	}
}

// TestDumpLive reads this machine's sockets (every Linux test host has
// at least one).
func TestDumpLive(t *testing.T) {
	s, err := Dump(syscall.IPPROTO_TCP, 1<<stateListen|1<<stateEstablished)
	if err != nil {
		t.Skip("sock_diag unavailable:", err)
	}
	for _, x := range s {
		if !x.Local.IsValid() || x.Proto != "tcp" {
			t.Fatalf("bad socket %+v", x)
		}
	}
}
