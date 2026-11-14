package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
)

// uninstallBus plays systemd for the uninstall units: starting
// hearth-purge@ rewrites dpkg's status as dpkg --purge would, the
// archive and restore units run their steps; units stopped and disabled
// are recorded.
type uninstallBus struct {
	t        *testing.T
	r        archive.Roots
	started  []string
	stopped  []string
	disabled []string
	failUnit string
	unit     string
}

func (b *uninstallBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	switch {
	case strings.HasSuffix(method, ".LoadUnit"):
		b.unit = args[0].(string)
		*out[0].(*dbus.ObjectPath) = "/unit"
	case strings.HasSuffix(method, ".GetUnit"):
		*out[0].(*dbus.ObjectPath) = "/unit"
	case strings.HasSuffix(method, ".Ref"), strings.HasSuffix(method, ".Reload"):
	case strings.HasSuffix(method, ".StopUnit"):
		b.stopped = append(b.stopped, args[0].(string))
	case strings.HasSuffix(method, ".DisableUnitFiles"):
		b.disabled = append(b.disabled, args[0].([]string)...)
	case strings.HasSuffix(method, ".StartUnit"):
		u := args[0].(string)
		b.started = append(b.started, u)
		if u == b.failUnit {
			return errors.New("failed to start")
		}
		op := strings.TrimSuffix(u[strings.IndexByte(u, '@')+1:], ".service")
		switch {
		case strings.HasPrefix(u, "hearth-purge@"):
			raw, err := os.ReadFile(filepath.Join(b.r.OpDir(op), purgeArgs))
			if err != nil {
				return err
			}
			names := strings.Fields(strings.TrimPrefix(strings.TrimSpace(string(raw)), "PURGE_ARGS="))
			st, _ := os.ReadFile(b.r.DpkgStatus)
			var keep []string
			for _, para := range strings.Split(string(st), "\n\n") {
				drop := false
				for _, n := range names {
					name, _, _ := strings.Cut(n, ":")
					if strings.HasPrefix(para, "Package: "+name+"\n") {
						drop = true
					}
				}
				if !drop && strings.TrimSpace(para) != "" {
					keep = append(keep, para)
				}
			}
			return os.WriteFile(b.r.DpkgStatus, []byte(strings.Join(keep, "\n\n")+"\n"), 0o600) // #nosec G703 -- a test fixture
		case strings.HasPrefix(u, "hearth-archive@"):
			return archive.RunArchive(context.Background(), b.r, op, copyTar(b.t))
		case strings.HasPrefix(u, "hearth-restore-leftovers@"):
			return archive.RunRestore(context.Background(), b.r, op, copyTar(b.t))
		}
	default:
		b.t.Fatalf("unexpected call %s", method)
	}
	return nil
}

func (b *uninstallBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("inactive"),
		"StateChangeTimestamp": dbus.MakeVariant(uint64(time.Now().Add(time.Hour).UnixMicro()))}, nil // #nosec G115 -- after 1970
}

func (b *uninstallBus) Close() error { return nil }

// copyTar "archives" a member by recording its name: the archive step's
// own tests cover tar; these cover the broker.
func copyTar(t *testing.T) archive.Tar {
	return func(_ context.Context, name string, args []string) ([]byte, bool, error) {
		file := args[slices.Index(args, "--file")+1]
		member := args[len(args)-1]
		switch name {
		case "leftovers-tar-create":
			return nil, false, os.WriteFile(file, []byte(member), 0o600)
		case "leftovers-tar-list":
			b, err := os.ReadFile(file) // #nosec G304 -- a test fixture
			return append(b, '\n'), false, err
		case "leftovers-tar-extract":
			dir := args[slices.Index(args, "-C")+1]
			return nil, false, os.MkdirAll(filepath.Join(dir, member), 0o750)
		}
		t.Fatalf("tar %s", name)
		return nil, false, nil
	}
}

type uninstallRig struct {
	t       *testing.T
	r       archive.Roots
	sys     string
	etc     string
	bus     *uninstallBus
	methods map[string]Method
	asked   int // polkit checks
	now     time.Time
}

func newUninstallRig(t *testing.T) *uninstallRig {
	t.Helper()
	archive.RootUID = uint32(os.Getuid()) // #nosec G115 -- a test uid
	t.Cleanup(func() { archive.RootUID = 0 })
	base := t.TempDir()
	g := &uninstallRig{t: t, sys: filepath.Join(base, "sys"), etc: filepath.Join(base, "sys/etc/systemd/system"), now: time.Now()}
	g.r = archive.Roots{Uninstall: filepath.Join(base, "uninstall"), Removed: filepath.Join(base, "removed"),
		DpkgStatus: filepath.Join(base, "status"), DpkgInfo: filepath.Join(base, "info"), System: g.sys}
	for _, d := range []string{"etc/foo", "opt/Foo/bin", "var/lib/foo", "usr/local/bin", "etc/systemd/system"} {
		if err := os.MkdirAll(filepath.Join(g.sys, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(g.r.DpkgInfo, 0o750); err != nil {
		t.Fatal(err)
	}
	g.write(g.r.DpkgStatus, "Package: foo\nStatus: install ok installed\nArchitecture: amd64\nConffiles:\n /etc/foo/foo.conf 1\n\n"+
		"Package: libfoo1\nStatus: install ok installed\nArchitecture: amd64\n\n"+
		"Package: bash\nStatus: install ok installed\nEssential: yes\nArchitecture: amd64\n\n"+
		"Package: hearth\nStatus: install ok installed\nArchitecture: amd64\n\n"+
		"Package: other\nStatus: install ok installed\nArchitecture: amd64\n")
	g.write(filepath.Join(g.r.DpkgInfo, "foo.list"), "/usr/bin/foo\n/usr/lib/systemd/system/foo.service\n/etc/foo\n/etc/foo/foo.conf\n")
	g.write(filepath.Join(g.r.DpkgInfo, "other.list"), "/var/lib/shared\n/var/lib/shared/x\n")
	g.write(filepath.Join(g.sys, "etc/foo/foo.conf"), "conf")
	g.write(filepath.Join(g.etc, "foo-web.service"), "[Service]\nExecStart=/opt/Foo/bin/web\n")
	g.write(filepath.Join(g.etc, "evil.service"), "[Service]\nExecStart=/usr/bin/sshd\n")
	if err := os.MkdirAll(filepath.Join(g.sys, "var/lib/shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	g.bus = &uninstallBus{t: t, r: g.r}
	g.methods = map[string]Method{}
	for _, m := range UninstallMethods(UninstallConfig{Roots: g.r, EtcUnits: g.etc, System: g.sys, Poll: time.Millisecond,
		Now: func() time.Time { return g.now },
		Bus: func(context.Context) (SystemBus, error) { return g.bus, nil }}) {
		g.methods[m.Name] = m
	}
	return g
}

func (g *uninstallRig) write(p, s string) {
	g.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil { // #nosec G703 -- a test fixture
		g.t.Fatal(err)
	}
}

func (g *uninstallRig) move(p string) archive.Move {
	g.t.Helper()
	fi, err := os.Lstat(filepath.Join(g.sys, p))
	if err != nil {
		g.t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return archive.Move{Path: p, Dev: st.Dev, Ino: st.Ino, Kind: archive.Kind(fi.Mode())}
}

// call runs a method as the server does: Check, which refuses before
// polkit only with bad params (any other answer goes on), then Authorized
// or polkit (always granted here, and counted), then Handle.
func (g *uninstallRig) call(uid uint32, name string, params any) (any, error) {
	g.t.Helper()
	m := g.methods[name]
	raw, err := json.Marshal(params)
	if err != nil {
		g.t.Fatal(err)
	}
	c := Caller{UID: uid}
	if m.Check != nil {
		if err := m.Check(context.Background(), raw); code(err) == CodeBadParams {
			return nil, err
		}
	}
	if m.Privileged {
		decided, ok := false, false
		if m.Authorized != nil {
			decided, ok = m.Authorized(context.Background(), c, raw)
		}
		if !decided {
			g.asked++
		} else if !ok {
			return nil, Errorf(CodeUnauthorized, "not authorized")
		}
	}
	return m.Handle(context.Background(), c, raw)
}

func (g *uninstallRig) authorize(uid uint32, p UninstallAuthorizeParams) (string, error) {
	res, err := g.call(uid, "uninstall.authorize", p)
	if err != nil {
		return "", err
	}
	return res.(map[string]any)["op"].(string), nil
}

func (g *uninstallRig) setConfigOnly(names ...string) {
	b, _ := os.ReadFile(g.r.DpkgStatus)
	s := string(b)
	for _, n := range names {
		s = strings.Replace(s, "Package: "+n+"\nStatus: install ok installed", "Package: "+n+"\nStatus: deinstall ok config-files", 1)
	}
	g.write(g.r.DpkgStatus, s)
}

func TestUninstallOnePromptForAPlan(t *testing.T) {
	g := newUninstallRig(t)
	g.write(filepath.Join(g.sys, "opt/Foo/bin/web"), "x")
	g.write(filepath.Join(g.sys, "var/lib/foo/state"), "x")
	plan := UninstallAuthorizeParams{Item: "Foo\x00\x1b[31m", Units: []string{"foo.service", "foo-web.service"}, Purge: []string{"foo:amd64", "libfoo1:amd64"},
		Archive: []archive.Move{g.move("/opt/Foo"), g.move("/var/lib/foo")}, Conffiles: []string{"foo:amd64"}}
	op, err := g.authorize(1000, plan)
	if err != nil {
		t.Fatal(err)
	}
	if g.asked != 1 {
		t.Fatalf("polkit asked %d times", g.asked)
	}
	if fi, err := os.Stat(g.r.OpDir(op)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatal("the plan folder must be 0700")
	}
	// Units, as the order of the run has it.
	for _, u := range plan.Units {
		if _, err := g.call(1000, "unit.disable", UnitDisableParams{Op: op, Unit: u}); err != nil {
			t.Fatal(u, err)
		}
	}
	if !slices.Equal(g.bus.stopped, plan.Units) || !slices.Equal(g.bus.disabled, plan.Units) {
		t.Fatalf("%v %v", g.bus.stopped, g.bus.disabled)
	}
	// Purge only once just the configuration is left.
	if _, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"foo:amd64"}}); code(err) != CodeConflict {
		t.Fatalf("purged an installed package: %v", err)
	}
	g.setConfigOnly("foo", "libfoo1")
	if _, err := g.call(1000, "leftovers.archive", ArchiveParams{Op: op, Conffiles: []string{"foo:amd64"}}); err != nil {
		t.Fatal(err)
	}
	res, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"foo:amd64"}})
	if err != nil || !slices.Equal(res.(map[string]any)["purged"].([]string), []string{"foo:amd64"}) {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(g.r.OpDir(op), purgeArgs)); !os.IsNotExist(err) {
		t.Error("the args file stays after the purge")
	}
	if _, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"libfoo1:amd64"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.call(1000, "leftovers.archive", ArchiveParams{Op: op, Paths: []string{"/opt/Foo", "/var/lib/foo"}}); err != nil {
		t.Fatal(err)
	}
	if g.asked != 1 {
		t.Fatalf("polkit asked %d times for one uninstall", g.asked)
	}
	for _, p := range []string{"opt/Foo", "var/lib/foo"} {
		if _, err := os.Lstat(filepath.Join(g.sys, p)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	// Each step runs once.
	if _, err := g.call(1000, "unit.disable", UnitDisableParams{Op: op, Unit: "foo.service"}); code(err) != CodeConflict {
		t.Fatalf("a step ran twice: %v", err)
	}
	if _, err := g.call(1000, "leftovers.archive", ArchiveParams{Op: op, Paths: []string{"/opt/Foo"}}); code(err) != CodeConflict {
		t.Fatalf("a step ran twice: %v", err)
	}
	// The list shows the item's name cleaned, to its user only.
	list, err := g.call(1000, "leftovers.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	ops := list.([]LeftoverOp)
	if len(ops) != 1 || ops[0].Item != "Foo[31m" || len(ops[0].Entries) != 3 {
		t.Fatalf("%+v", ops)
	}
	if other, _ := g.call(1001, "leftovers.list", nil); len(other.([]LeftoverOp)) != 0 {
		t.Fatal("another user sees the archive")
	}
	// Restore asks polkit again; another user may not.
	if _, err := g.call(1001, "leftovers.restore", RestoreLeftoversParams{Op: op, Paths: []string{"/opt/Foo"}}); code(err) != CodeUnauthorized {
		t.Fatalf("another user restored: %v", err)
	}
	asked := g.asked
	res, err = g.call(1000, "leftovers.restore", RestoreLeftoversParams{Op: op, Paths: []string{"/opt/Foo"}})
	if err != nil || g.asked != asked+1 {
		t.Fatalf("%v %v", res, err)
	}
	if r := res.(map[string]any)["results"].([]archive.RestoreResult); len(r) != 1 || !r[0].Done {
		t.Fatalf("%+v", r)
	}
}

func TestUninstallRefusedBeforePolkit(t *testing.T) {
	g := newUninstallRig(t)
	g.write(filepath.Join(g.sys, "opt/Foo/bin/web"), "x")
	good := func() UninstallAuthorizeParams {
		return UninstallAuthorizeParams{Item: "Foo", Purge: []string{"foo:amd64"}, Archive: []archive.Move{g.move("/opt/Foo")}}
	}
	swapped := g.move("/var/lib/foo")
	swapped.Ino++
	cases := map[string]func(*UninstallAuthorizeParams){
		"nothing":      func(p *UninstallAuthorizeParams) { p.Purge, p.Archive = nil, nil },
		"no item":      func(p *UninstallAuthorizeParams) { p.Item = "\x01" },
		"long item":    func(p *UninstallAuthorizeParams) { p.Item = strings.Repeat("x", 81) },
		"essential":    func(p *UninstallAuthorizeParams) { p.Purge = []string{"bash"} },
		"hearth":       func(p *UninstallAuthorizeParams) { p.Purge = []string{"hearth:amd64"} },
		"unknown":      func(p *UninstallAuthorizeParams) { p.Purge = []string{"nope"} },
		"dash":         func(p *UninstallAuthorizeParams) { p.Purge = []string{"-rf"} },
		"slash":        func(p *UninstallAuthorizeParams) { p.Purge = []string{"a/b"} },
		"dots":         func(p *UninstallAuthorizeParams) { p.Purge = []string{".."} },
		"twice":        func(p *UninstallAuthorizeParams) { p.Purge = []string{"foo:amd64", "foo:amd64"} },
		"traversal":    func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/opt/../etc" },
		"usr":          func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/usr/bin" },
		"deep":         func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/opt/Foo/bin" },
		"etc itself":   func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/etc" },
		"shared":       func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/etc/systemd" },
		"owned":        func(p *UninstallAuthorizeParams) { p.Archive = []archive.Move{g.move("/var/lib/shared")} },
		"owned by foo": func(p *UninstallAuthorizeParams) { p.Archive = []archive.Move{g.move("/etc/foo")} },
		"swapped":      func(p *UninstallAuthorizeParams) { p.Archive = []archive.Move{swapped} },
		"missing":      func(p *UninstallAuthorizeParams) { p.Archive[0].Path = "/opt/Gone" },
		"no kind":      func(p *UninstallAuthorizeParams) { p.Archive[0].Kind = "" },
		"unknown kind": func(p *UninstallAuthorizeParams) { p.Archive[0].Kind = "socket" },
		// The folder's own device and inode, as a filesystem that reuses
		// inode numbers gives them to what replaces it.
		"another kind":   func(p *UninstallAuthorizeParams) { p.Archive[0].Kind = "link" },
		"unit unrelated": func(p *UninstallAuthorizeParams) { p.Units = []string{"evil.service"} },
		"unit sshd":      func(p *UninstallAuthorizeParams) { p.Units = []string{"ssh.service"} },
		"unit hearth":    func(p *UninstallAuthorizeParams) { p.Units = []string{"hearth-broker.service"} },
		"unit dbus":      func(p *UninstallAuthorizeParams) { p.Units = []string{"dbus.service"} },
		"unit template":  func(p *UninstallAuthorizeParams) { p.Units = []string{"foo@.service"} },
		"unit target":    func(p *UninstallAuthorizeParams) { p.Units = []string{"graphical.target"} },
		"unit flag":      func(p *UninstallAuthorizeParams) { p.Units = []string{"--now"} },
		"conf not purge": func(p *UninstallAuthorizeParams) { p.Conffiles = []string{"other"} },
	}
	for name, f := range cases {
		p := good()
		f(&p)
		if _, err := g.authorize(1000, p); err == nil {
			t.Errorf("%s: authorized", name)
		}
	}
	for _, raw := range []string{`{"item":"x","purge":["foo"],"extra":1}`, `[]`, `{"item":"x","purge":"foo"}`} {
		if err := g.methods["uninstall.authorize"].Check(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if g.asked != 0 {
		t.Fatalf("polkit was asked %d times for refused plans", g.asked)
	}
	// A leftover replaced by a link to /etc between preview and
	// authorization is refused, still before polkit.
	mv := g.move("/opt/Foo")
	if err := os.RemoveAll(filepath.Join(g.sys, "opt/Foo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(g.sys, "opt/Foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.authorize(1000, UninstallAuthorizeParams{Item: "Foo", Archive: []archive.Move{mv}}); err == nil || g.asked != 0 {
		t.Fatalf("a swapped leftover was authorized: %v", err)
	}
}

func TestUninstallPlanBounds(t *testing.T) {
	g := newUninstallRig(t)
	g.write(filepath.Join(g.sys, "opt/Foo/bin/web"), "x")
	op, err := g.authorize(1000, UninstallAuthorizeParams{Item: "Foo", Purge: []string{"foo:amd64"}, Archive: []archive.Move{g.move("/opt/Foo")}})
	if err != nil {
		t.Fatal(err)
	}
	asked := g.asked
	g.setConfigOnly("foo", "libfoo1")
	for name, c := range map[string]struct {
		uid    uint32
		method string
		params any
	}{
		"other user":    {1001, "pkg.purge", PurgeParams{Op: op, Names: []string{"foo:amd64"}}},
		"not in plan":   {1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"libfoo1:amd64"}}},
		"unit not plan": {1000, "unit.disable", UnitDisableParams{Op: op, Unit: "foo.service"}},
		"path not plan": {1000, "leftovers.archive", ArchiveParams{Op: op, Paths: []string{"/var/lib/foo"}}},
		"bad op":        {1000, "pkg.purge", PurgeParams{Op: "../" + op[3:], Names: []string{"foo:amd64"}}},
		"no op":         {1000, "pkg.purge", PurgeParams{Op: strings.Repeat("f", 32), Names: []string{"foo:amd64"}}},
	} {
		if _, err := g.call(c.uid, c.method, c.params); err == nil {
			t.Errorf("%s: ran", name)
		}
	}
	if g.asked != asked {
		t.Fatal("a step outside the plan asked polkit; it must be refused instead")
	}
	g.now = g.now.Add(UninstallPlanTTL + time.Second)
	if _, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"foo:amd64"}}); err == nil {
		t.Fatal("an expired plan was used")
	}
	if len(g.bus.started) != 0 {
		t.Fatalf("started %v", g.bus.started)
	}
}

func TestUninstallRestoreChecks(t *testing.T) {
	g := newUninstallRig(t)
	for _, p := range []RestoreLeftoversParams{
		{Op: "x", Paths: []string{"/opt/Foo"}}, {Op: strings.Repeat("a", 32), Paths: nil},
		{Op: strings.Repeat("a", 32), Paths: []string{"/opt/Foo"}}, // no such archive
	} {
		if _, err := g.call(1000, "leftovers.restore", p); err == nil {
			t.Errorf("%+v: ran", p)
		}
	}
	if g.asked != 0 {
		t.Fatal("polkit asked for a refused restore")
	}
}

// A dependency removed with no configuration files is gone from dpkg's
// status: purging it is already done, and no unit starts for it alone.
func TestUninstallPurgeOfForgottenPackage(t *testing.T) {
	g := newUninstallRig(t)
	op, err := g.authorize(1000, UninstallAuthorizeParams{Item: "Foo", Purge: []string{"libfoo1:amd64", "foo:amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(g.r.DpkgStatus)
	g.write(g.r.DpkgStatus, strings.Replace(string(b), "Package: libfoo1\nStatus: install ok installed\nArchitecture: amd64\n\n", "", 1))
	res, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"libfoo1:amd64"}})
	if err != nil || !slices.Equal(res.(map[string]any)["purged"].([]string), []string{"libfoo1:amd64"}) || len(g.bus.started) != 0 {
		t.Fatalf("%v %v %v", res, err, g.bus.started)
	}
	// Still installed: refused, never purged.
	if _, err := g.call(1000, "pkg.purge", PurgeParams{Op: op, Names: []string{"foo:amd64"}}); code(err) != CodeConflict {
		t.Fatalf("%v", err)
	}
}
