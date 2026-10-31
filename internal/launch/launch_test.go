package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestParseExec(t *testing.T) {
	f := fields{Name: "Text Editor", Icon: "org.gnome.TextEditor", Path: "/usr/share/applications/x.desktop"}
	cases := []struct {
		raw  string
		want []string
	}{
		{"gnome-text-editor %U", []string{"gnome-text-editor"}},
		{"code --new-window %F", []string{"code", "--new-window"}},
		{"/usr/bin/flatpak run --branch=stable --command=zen app.zen_browser.zen @@u %u @@", []string{"/usr/bin/flatpak", "run", "--branch=stable", "--command=zen", "app.zen_browser.zen", "@@u", "@@"}},
		{`app %i --name=%c --file=%k`, []string{"app", "--icon", "org.gnome.TextEditor", "--name=Text Editor", "--file=/usr/share/applications/x.desktop"}},
		{`"/opt/My App/run" --flag`, []string{"/opt/My App/run", "--flag"}},
		{`app "say \"hi\"" "a\\\\b" "$HOME" "100%"`, []string{"app", `say "hi"`, `a\b`, "$HOME", "100%"}},
		{`app 100%% x%dy`, []string{"app", "100%", "xy"}},
		{`app\sone two\ttabs`, []string{"app", "one", "two", "tabs"}},
		{`app $(rm -rf ~) ; echo | x`, []string{"app", "$(rm", "-rf", "~)", ";", "echo", "|", "x"}},
	}
	for _, c := range cases {
		got, err := parseExec(c.raw, f)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q %v, want %q", c.raw, got, err, c.want)
		}
	}
	if got, _ := parseExec("app %i", fields{}); !slices.Equal(got, []string{"app"}) {
		t.Errorf("%%i without an icon: %q", got)
	}
	for _, bad := range []string{"", "   ", `"unterminated`, "app %z", "app %", "%U"} {
		if got, err := parseExec(bad, f); err == nil {
			t.Errorf("%q accepted: %q", bad, got)
		}
	}
}

func FuzzParseExec(f *testing.F) {
	for _, s := range []string{"app %U", `"a b" \"c`, "x %% %i %c", `a\\s"\$"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		argv, err := parseExec(s, fields{Name: "n", Icon: "i", Path: "/p"})
		if err == nil && (len(argv) == 0 || argv[0] == "" || len(argv) > maxArgs) {
			t.Fatalf("%q -> %q", s, argv)
		}
	})
}

func TestEscapeUnit(t *testing.T) {
	for in, want := range map[string]string{
		"org.gnome.TextEditor": "org.gnome.TextEditor", "firefox-esr": `firefox\x2desr`,
		"appimage.obsidian-1.5.3": `appimage.obsidian\x2d1.5.3`, "a@b+c~": `a\x40b\x2bc\x7e`, ".x": `\x2ex`,
	} {
		if got := escapeUnit(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	u, err := unitName("firefox-esr")
	if err != nil || !strings.HasPrefix(u, `app-hearth-firefox\x2desr@`) || !strings.HasSuffix(u, ".service") || safeexec.UnitName(u) != nil {
		t.Fatalf("%s %v", u, err)
	}
	if _, err := unitName(strings.Repeat("-", 200)); err == nil {
		t.Fatal("over-long unit name accepted")
	}
}

type apps map[string]discovery.App

func (a apps) App(id string) (discovery.App, bool) { x, ok := a[id]; return x, ok }

type fixture struct {
	root string
	l    *launcher
}

func newFixture(t *testing.T) fixture {
	root := t.TempDir()
	write := func(rel string, mode os.FileMode) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/true\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("bin/editor", 0o700)
	write("bin/sh", 0o700)
	write("bin/env", 0o700)
	write("bin/htop", 0o700)
	write("bin/term", 0o700)
	write("home/.local/bin/mine", 0o700)
	write("home/Apps/Tool 1.0.AppImage", 0o700)
	write("home/Apps/NoExec.AppImage", 0o600)
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := apps{
		"editor":   {ID: "editor", Name: "Editor", Exec: "editor --new %U", Path: "/x/editor.desktop", WorkDir: filepath.Join(root, "work")},
		"mine":     {ID: "mine", Name: "Mine", Exec: "mine", WorkDir: "/does/not/exist"},
		"shell":    {ID: "shell", Name: "Shell", Exec: `sh -ec "editor; rm -rf ~"`},
		"envshell": {ID: "envshell", Name: "EnvShell", Exec: `env FOO=1 -i /bin/dash script.sh`},
		"envok":    {ID: "envok", Name: "EnvOK", Exec: `env GDK_BACKEND=x11 editor`},
		"htop":     {ID: "htop", Name: "htop", Exec: "htop", Terminal: true},
		"missing":  {ID: "missing", Name: "Missing", Exec: "not-installed"},
		"relative": {ID: "relative", Name: "Relative", Exec: "./run"},
		"control":  {ID: "control", Name: "Control", Exec: `editor "a\nb"`},
		"appimage.tool": {ID: "appimage.tool", Name: "Tool", Source: discovery.SourceAppImage,
			Path: filepath.Join(root, "home/Apps/Tool 1.0.AppImage")},
		"appimage.noexec": {ID: "appimage.noexec", Name: "NoExec", Source: discovery.SourceAppImage,
			Path: filepath.Join(root, "home/Apps/NoExec.AppImage")},
	}
	l := &launcher{cfg: Config{Apps: a, Home: filepath.Join(root, "home"), Path: []string{"relative/dir", filepath.Join(root, "bin")},
		Terminal: filepath.Join(root, "bin/term"), Wait: time.Second}}
	return fixture{root: root, l: l}
}

func TestCommand(t *testing.T) {
	fx := newFixture(t)
	bin := filepath.Join(fx.root, "bin")
	home := filepath.Join(fx.root, "home")
	cmd := func(id string) (command, error) {
		a, _ := fx.l.cfg.Apps.App(id)
		return fx.l.command(a, "")
	}
	c, err := cmd("editor")
	if err != nil || c.prog != bin+"/editor" || !slices.Equal(c.argv, []string{"editor", "--new"}) || c.dir != fx.root+"/work" || c.name != "Editor" {
		t.Errorf("editor: %+v %v", c, err)
	}
	if c, err := cmd("mine"); err != nil || c.prog != home+"/.local/bin/mine" || c.dir != home {
		t.Errorf("mine: %+v %v", c, err)
	}
	if c, err := cmd("envok"); err != nil || c.prog != bin+"/env" {
		t.Errorf("envok: %+v %v", c, err)
	}
	if c, err := cmd("htop"); err != nil || c.prog != bin+"/term" || !slices.Equal(c.argv, []string{bin + "/term", "-e", "htop"}) {
		t.Errorf("terminal: %+v %v", c, err)
	}
	if c, err := cmd("appimage.tool"); err != nil || c.prog != home+"/Apps/Tool 1.0.AppImage" || len(c.argv) != 1 {
		t.Errorf("appimage: %+v %v", c, err)
	}
	for id, want := range map[string]string{
		"shell": "shell", "envshell": "shell", "missing": "not installed", "relative": "relative path",
		"control": "control characters", "appimage.noexec": "not executable",
	} {
		if _, err := cmd(id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", id, err, want)
		}
	}
	fx.l.cfg.Terminal = "/nonexistent/term"
	if _, err := cmd("htop"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("no terminal: %v", err)
	}
}

// fakeBus plays the systemd user manager.
type fakeBus struct {
	mu     sync.Mutex
	calls  []string
	unit   string
	props  map[string]any
	result string // JobRemoved result; "" sends none
	sigs   chan *dbus.Signal
	closed bool
}

func (b *fakeBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, errors.New("unused")
}
func (b *fakeBus) Set(context.Context, string, dbus.ObjectPath, string, string, any) error {
	return errors.New("unused")
}
func (b *fakeBus) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	return b.sigs, nil
}
func (b *fakeBus) Close() error { b.closed = true; return nil }

func (b *fakeBus) Call(_ context.Context, dest string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, method)
	if !strings.HasSuffix(method, ".StartTransientUnit") {
		return nil
	}
	b.unit = args[0].(string)
	b.props = map[string]any{}
	for _, p := range args[2].([]property) {
		b.props[p.Name] = p.Value.Value()
	}
	*out[0].(*dbus.ObjectPath) = "/org/freedesktop/systemd1/job/7"
	// Another job's end first, then ours.
	b.sigs <- &dbus.Signal{Name: manager + ".JobRemoved", Body: []any{uint32(6), dbus.ObjectPath("/org/freedesktop/systemd1/job/6"), "x.service", "failed"}}
	if b.result != "" {
		b.sigs <- &dbus.Signal{Name: manager + ".JobRemoved", Body: []any{uint32(7), dbus.ObjectPath("/org/freedesktop/systemd1/job/7"), b.unit, b.result}}
	}
	return nil
}

func TestLaunch(t *testing.T) {
	fx := newFixture(t)
	bus := &fakeBus{result: "done", sigs: make(chan *dbus.Signal, 4)}
	fx.l.cfg.Bus = func(context.Context) (Bus, error) { return bus, nil }
	req := actions.Request{Target: "editor", Params: map[string]string{}}
	p, err := fx.l.plan(context.Background(), req)
	if err != nil || p.Items[0].Detail != "editor --new" {
		t.Fatalf("plan %+v %v", p, err)
	}
	res, err := fx.l.run(context.Background(), req, p)
	if err != nil || res.Done != 1 {
		t.Fatalf("run %+v %v", res, err)
	}
	if !strings.HasPrefix(bus.unit, "app-hearth-editor@") || !bus.closed ||
		!slices.Equal(bus.calls, []string{manager + ".Subscribe", manager + ".StartTransientUnit"}) {
		t.Fatalf("bus %+v", bus)
	}
	ex := bus.props["ExecStartEx"].([]execEx)
	if len(ex) != 1 || ex[0].Path != fx.root+"/bin/editor" || !slices.Equal(ex[0].Argv, []string{"editor", "--new"}) ||
		!slices.Equal(ex[0].Flags, []string{"no-env-expand"}) {
		t.Errorf("ExecStartEx %+v", ex)
	}
	for k, want := range map[string]string{"Type": "exec", "ExitType": "cgroup", "Slice": "app.slice",
		"CollectMode": "inactive-or-failed", "WorkingDirectory": fx.root + "/work"} {
		if bus.props[k] != want {
			t.Errorf("%s = %v, want %s", k, bus.props[k], want)
		}
	}

	bus = &fakeBus{result: "failed", sigs: make(chan *dbus.Signal, 4)}
	if _, err := fx.l.run(context.Background(), req, p); err == nil || !strings.Contains(err.Error(), "could not be started (failed)") {
		t.Errorf("failed job: %v", err)
	}
	bus = &fakeBus{sigs: make(chan *dbus.Signal, 4)}
	fx.l.cfg.Wait = 50 * time.Millisecond
	if _, err := fx.l.run(context.Background(), req, p); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Errorf("no job end: %v", err)
	}
	if _, err := fx.l.plan(context.Background(), actions.Request{Target: "nope"}); err == nil {
		t.Error("unknown app planned")
	}
}

func TestRegister(t *testing.T) {
	reg := actions.NewRegistry(nil)
	Register(reg, Config{Apps: apps{}})
	// Registering twice panics: the action exists once.
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration accepted")
		}
	}()
	Register(reg, Config{Apps: apps{}})
}

type games map[string][3]string // id -> name, app ID, URL

func (g games) LaunchTarget(id string) (string, string, string, bool) {
	v, ok := g[id]
	return v[0], v[1], v[2], ok
}

func TestLaunchGame(t *testing.T) {
	fx := newFixture(t)
	fx.l.cfg.Games = games{
		"steam.220":    {"Half-Life 2", "editor", "steam://rungameid/220"},
		"steam.10":     {"Counter-Strike", "", ""},
		"steam.20":     {"Gone", "not-an-app", "steam://rungameid/20"},
		"desktop.mine": {"Mine", "mine", ""},
		"steam.30":     {"Tool", "appimage.tool", "steam://rungameid/30"},
	}
	bus := &fakeBus{result: "done", sigs: make(chan *dbus.Signal, 4)}
	fx.l.cfg.Bus = func(context.Context) (Bus, error) { return bus, nil }
	req := actions.Request{Target: "steam.220"}
	p, err := fx.l.planGame(context.Background(), req)
	// %U becomes the URL Hearth built, once.
	if err != nil || p.Items[0].Detail != "editor --new steam://rungameid/220" || p.Summary != "launch Half-Life 2" {
		t.Fatalf("plan %+v %v", p, err)
	}
	if _, err := fx.l.run(context.Background(), req, p); err != nil {
		t.Fatal(err)
	}
	// The unit is the launcher's, so the process view shows it as that app.
	if !strings.HasPrefix(bus.unit, "app-hearth-editor@") {
		t.Errorf("unit %s", bus.unit)
	}
	if p, err := fx.l.planGame(context.Background(), actions.Request{Target: "steam.30"}); err != nil ||
		p.Items[0].Detail != fx.root+"/home/Apps/Tool 1.0.AppImage steam://rungameid/30" {
		t.Errorf("appimage launcher: %+v %v", p, err)
	}
	if p, err := fx.l.planGame(context.Background(), actions.Request{Target: "desktop.mine"}); err != nil || p.Items[0].Detail != "mine" {
		t.Errorf("desktop game: %+v %v", p, err)
	}
	for id, want := range map[string]string{"steam.10": "launcher is not installed", "steam.20": "launcher is not installed", "steam.99": "unknown game"} {
		if _, err := fx.l.planGame(context.Background(), actions.Request{Target: id}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", id, err, want)
		}
	}

	reg := actions.NewRegistry(nil)
	Register(reg, Config{Apps: apps{}, Games: games{}})
}

func TestExecURL(t *testing.T) {
	for raw, want := range map[string][]string{
		"steam %U":     {"steam", "steam://rungameid/1"},
		"lutris %u %U": {"lutris", "steam://rungameid/1"},
		"flatpak run --file-forwarding x @@u %U @@": {"flatpak", "run", "--file-forwarding", "x", "@@u", "steam://rungameid/1", "@@"},
		"app %f":   {"app"},
		`app "%U"`: {"app", "%U"},
	} {
		got, err := parseExec(raw, fields{URL: "steam://rungameid/1"})
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %q %v, want %q", raw, got, err, want)
		}
	}
}
