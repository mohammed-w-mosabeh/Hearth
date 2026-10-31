package netinfo

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

type fakePkgs map[string]string // path -> owner

func (f fakePkgs) Owners(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		if o, ok := f[p]; ok {
			out[p] = o
		}
	}
	return out, nil
}

func (f fakePkgs) ByName(name string) ([]discovery.Package, error) {
	return []discovery.Package{{Name: name, Arch: "amd64", Version: "1.2-3"}}, nil
}

func lineageConfig(t *testing.T) (Config, *Resolver) {
	t.Helper()
	r, _ := fakeSystem(t)
	v := dbus.MakeVariant
	sys := &fakeBus{
		units: map[string]dbus.ObjectPath{"ssh.service": "/u/ssh", "itest.service": "/u/itest"},
		props: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
			"/u/ssh": {
				"org.freedesktop.systemd1.Unit": {"Description": v("OpenSSH <server>"), "ActiveState": v("active"),
					"SubState": v("running"), "FragmentPath": v("/usr/lib/systemd/system/ssh.service")},
				"org.freedesktop.systemd1.Service": {"MainPID": v(uint32(300)),
					"ExecStart": v([][]any{{"/usr/sbin/sshd", []string{"sshd", "-D"}, false, uint64(0), uint64(0), uint64(0), uint64(0), uint32(0), int32(0), int32(0)}})},
			},
			"/u/itest": {"org.freedesktop.systemd1.Unit": {"FragmentPath": v(""), "ActiveState": v("active")}},
		},
	}
	user := &fakeBus{}
	c := Config{Resolver: r, Log: slog.Default(),
		Bus:      func(context.Context) (dbusx.Bus, error) { return sys, nil },
		Session:  func(context.Context) (dbusx.Bus, error) { return user, nil },
		Packages: fakePkgs{"/usr/lib/systemd/system/ssh.service": "openssh-server", "/usr/sbin/sshd": "openssh-server", "/usr/bin/chat": "chat:amd64"}}
	return c, r
}

func getLineage(t *testing.T, c Config, q string) (int, Lineage) {
	t.Helper()
	w := httptest.NewRecorder()
	c.HandleLineage(w, httptest.NewRequest("GET", "/api/v1/net/lineage?"+q, nil))
	var l Lineage
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, l
}

func TestLineage(t *testing.T) {
	c, r := lineageConfig(t)
	// Another user's service: the program is the unit's ExecStart.
	code, l := getLineage(t, c, "scope=system&unit=ssh.service&pid=300&start=5000")
	if code != 200 || l.Unit == nil || l.Unit.File != "/usr/lib/systemd/system/ssh.service" || l.Unit.MainPID != 300 ||
		l.Unit.Description != "OpenSSH <server>" || l.Exe != "/usr/sbin/sshd" || l.ExeExact ||
		l.UnitPackage == nil || l.UnitPackage.ID != "openssh-server:amd64" || l.UnitPackage.Version != "1.2-3" ||
		l.ExePackage == nil || l.ExePackage.Name != "openssh-server" {
		t.Fatalf("%d %+v %+v", code, l, l.Unit)
	}
	// A transient unit: no file, no package.
	if code, l := getLineage(t, c, "scope=system&unit=itest.service"); code != 200 || l.Unit == nil || l.Unit.File != "" || l.UnitPackage != nil || l.Exe != "" {
		t.Errorf("transient: %d %+v", code, l)
	}
	// A unit that is not loaded, of the user manager.
	if code, l := getLineage(t, c, "scope=user&unit=gone.service"); code != 200 || l.Unit != nil {
		t.Errorf("not loaded: %d %+v", code, l)
	}
	// The user's own process: its exe link, checked against its start time.
	if err := os.Symlink("/usr/bin/chat", filepath.Join(r.ProcRoot, "200/exe")); err != nil {
		t.Fatal(err)
	}
	if code, l := getLineage(t, c, "pid=200&start=5000"); code != 200 || l.Exe != "/usr/bin/chat" || !l.ExeExact ||
		l.ExePackage == nil || l.ExePackage.ID != "chat:amd64" {
		t.Errorf("own: %d %+v", code, l)
	}
	if code, l := getLineage(t, c, "pid=200&start=4999"); code != 200 || l.Exe != "" {
		t.Errorf("reused pid: %d %+v", code, l)
	}
	for _, q := range []string{"", "unit=ssh.service", "scope=root&unit=ssh.service", "scope=system&unit=-H",
		"scope=system&unit=../x", "pid=abc&start=1", "pid=-1&start=1", "pid=1"} {
		if code, _ := getLineage(t, c, q); code != 400 {
			t.Errorf("%q: %d", q, code)
		}
	}
}

func TestExecPath(t *testing.T) {
	v := dbus.MakeVariant
	if p := execPath(v([][]any{{"/usr/bin/../sbin/x", []string{}}})); p != "/usr/sbin/x" {
		t.Error(p)
	}
	for _, bad := range []dbus.Variant{v("x"), v([][]any{}), v([][]any{{"relative"}}), v([][]any{{}})} {
		if p := execPath(bad); p != "" {
			t.Errorf("%v: %q", bad, p)
		}
	}
}
