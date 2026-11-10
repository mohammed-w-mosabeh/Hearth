package uninstall

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
)

func TestRunningParts(t *testing.T) {
	l := local(t)
	etc := t.TempDir()
	opt := "/opt/Foo"
	res := Resolved{Owner: Owner{Manager: ManagerApt}, Identifiers: Identifiers{
		Packages: []string{"foo"}, Exec: []string{"foo"}, Names: []string{"Foo"}, Desktop: []string{"foo", "org.foo.Foo"}, AppIDs: []string{"org.foo.Foo"},
	}}
	program := []Candidate{{Group: GroupProgram, Kind: KindPackage, ID: "foo:amd64"}, {Group: GroupProgram, Kind: KindArchive, ID: opt}}
	var asked []string
	r := Running{
		Local: l, Self: 99, EtcUnits: etc,
		Procs: func(ids []string) ([]collect.ProcRef, error) {
			asked = ids
			return []collect.ProcRef{
				{PID: 10, Start: 1, Name: "foo", Own: true, Group: "app:org.foo.Foo"},
				{PID: 11, Start: 2, Name: "foo-helper", Own: false, User: "foo", Group: "exe:/usr/bin/foo"},
				{PID: 99, Start: 3, Name: "hearthd", Own: true},
				{PID: 12, Start: 4, Name: "foo", Own: true, Cgroup: "/user.slice/user-1000.slice/user@1000.service/session.slice/x.service"},
			}, nil
		},
		UnitState: func(user bool, name string) (string, string, bool) {
			switch name {
			case "foo.service", "Foo.service", "foo-web.service":
				return "active", "enabled", true
			case "foo-idle.service":
				return "inactive", "disabled", true
			case "my-foo.service", "foo.timer":
				return "inactive", "enabled", true
			}
			return "", "", false
		},
		PackageFiles: func(id string) []string {
			return []string{"/usr/bin/foo", "/usr/lib/systemd/system/foo.service", "/usr/lib/systemd/system/foo-idle.service",
				"/usr/lib/systemd/system/foo@.service", "/usr/lib/systemd/system/foo.timer", "/usr/share/doc/foo/README"}
		},
	}
	write(t, filepath.Join(etc, "Foo.service"), "[Service]\nExecStart=-/opt/Foo/bin/web --port 1\n")
	write(t, filepath.Join(etc, "foo-web.service"), "[Service]\nExecStart=/opt/Foo/bin/web\n") // not exactly named like the item
	write(t, filepath.Join(etc, "foo.target"), "[Unit]\n")
	write(t, filepath.Join(etc, "other.service"), "[Service]\nExecStart=/opt/Foo/bin/web\n") // not named like the item
	write(t, filepath.Join(etc, "foo.socket"), "[Socket]\nExecStart=/usr/bin/elsewhere\n")   // starts something else
	userDir := filepath.Join(l.Home, ".config/systemd/user")
	write(t, filepath.Join(userDir, "my-foo.service"), "[Service]\nExecStart=/usr/bin/foo --daemon\n") // name does not match
	write(t, filepath.Join(userDir, "foo.timer"), "[Timer]\nExecStart=/usr/bin/foo\n")
	mkdir(t, filepath.Join(userDir, "foo.timer.d"))
	auto := filepath.Join(l.Home, ".config/autostart")
	write(t, filepath.Join(auto, "org.foo.Foo.desktop"), "[Desktop Entry]\nExec=flatpak run org.foo.Foo\n")
	write(t, filepath.Join(auto, "tray.desktop"), "[Desktop Entry]\nExec=/opt/Foo/tray --min\n")
	write(t, filepath.Join(auto, "byname.desktop"), "[Desktop Entry]\nExec=foo --tray\n")
	write(t, filepath.Join(auto, "other.desktop"), "[Desktop Entry]\nExec=/usr/bin/bar\n")
	if err := os.Symlink(filepath.Join(auto, "tray.desktop"), filepath.Join(auto, "link.desktop")); err != nil {
		t.Fatal(err)
	}

	got := r.Parts(res, program)
	ids := map[string]Candidate{}
	for _, c := range got {
		ids[c.Group+" "+c.ID] = c
	}
	for _, want := range []string{
		"running pid:10:1", "remains pid:11:2", "remains pid:12:4",
		"running system/foo.service", "running system/foo.timer", "running system/Foo.service", "remains " + filepath.Join(etc, "Foo.service"),
		"running user/foo.timer", "running " + filepath.Join(userDir, "foo.timer"), "running " + filepath.Join(userDir, "foo.timer.d"),
		"running " + filepath.Join(auto, "org.foo.Foo.desktop"), "running " + filepath.Join(auto, "tray.desktop"),
		"running " + filepath.Join(auto, "byname.desktop"),
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if len(ids) != 13 {
		for k := range ids {
			t.Log(k)
		}
		t.Fatalf("%d parts", len(ids))
	}
	if ids["remains pid:11:2"].Warn != "blocks" || ids["remains pid:12:4"].Warn != "blocks" {
		t.Error("another user's or the session's processes must block")
	}
	for _, g := range []string{"app:foo", "app:org.foo.Foo", "exe:/usr/bin/foo", "exe:/opt/Foo"} {
		if !slices.Contains(asked, g) {
			t.Errorf("did not look for %s in %v", g, asked)
		}
	}
}

func TestExecStart(t *testing.T) {
	for in, want := range map[string]string{
		"ExecStart=/usr/bin/x -a\n":               "/usr/bin/x",
		"ExecStart=\nExecStart=-@/opt/a/b arg0\n": "/opt/a/b",
		"ExecStartPre=/bin/true\nExecStart=+!/x":  "/x",
		"[Service]\nType=simple\n":                "",
	} {
		if got := execStart([]byte(in)); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
