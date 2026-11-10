package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// fakeGSettings is gsettings over a map; it checks every call against the
// real allowlist's validators.
type fakeGSettings struct {
	values map[string]string
	calls  []string
	fail   bool
}

func (f *fakeGSettings) run(_ context.Context, name string, args ...string) (safeexec.Result, error) {
	if err := safeexec.Check(name, args...); err != nil {
		return safeexec.Result{}, err
	}
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fail {
		return safeexec.Result{ExitCode: 1}, nil
	}
	key := args[1] + " " + args[2]
	if args[0] == "set" {
		f.values[key] = args[3]
		return safeexec.Result{}, nil
	}
	return safeexec.Result{Stdout: []byte(f.values[key] + "\n")}, nil
}

func newFake() *fakeGSettings {
	return &fakeGSettings{values: map[string]string{
		"org.gnome.desktop.background picture-uri":      "'file:///usr/share/backgrounds/gnome/blobs-l.svg'",
		"org.gnome.desktop.background picture-uri-dark": "'file:///usr/share/backgrounds/gnome/blobs-d.svg'",
		"org.gnome.desktop.notifications show-banners":  "true",
	}}
}

type noAudit struct{}

func (noAudit) Audit(context.Context, store.AuditEntry) error { return nil }

func TestDetectDesktop(t *testing.T) {
	for in, want := range map[string]Desktop{
		"GNOME":            {"GNOME", true},
		"ubuntu:GNOME":     {"GNOME", true},
		"KDE":              {"KDE", false},
		"XFCE":             {"XFCE", false},
		"":                 {"unknown", false},
		"\x1b[2J":          {"unknown", false},
		"evil\nname:GNOME": {"GNOME", true},
	} {
		if got := DetectDesktop(in); got != want {
			t.Errorf("%q: %+v", in, got)
		}
	}
}

func TestCheckWallpaper(t *testing.T) {
	for _, ok := range []string{"~/Pictures/a.jpg", "~/a b/ç.PNG", "/usr/share/backgrounds/gnome/blobs-l.svg", "/usr/share/wallpapers/x.webp"} {
		if err := CheckWallpaper(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "~/", "/etc/a.png", "~/../a.png", "~//etc/a.png", "~/a/../../b.png", "/usr/share/backgrounds/../../../etc/a.png",
		"~/.bashrc", "~/a\x00.png", "~/a\u202e.png", "relative.png", "/home/u/a.png"} {
		if CheckWallpaper(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestResolveWallpaper(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(home, "Pictures", "a.jpg"))
	mk(filepath.Join(outside, "secret.png"))
	_ = os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(home, "Pictures", "out.png"))
	_ = os.Symlink(filepath.Join(home, "Pictures", "a.jpg"), filepath.Join(home, "Pictures", "in.png"))
	_ = os.Mkdir(filepath.Join(home, "dir.png"), 0o700)
	s := New(Config{Home: home})
	if got, err := s.ResolveWallpaper("~/Pictures/a.jpg"); err != nil || got != filepath.Join(home, "Pictures", "a.jpg") {
		t.Fatal(got, err)
	}
	if got, err := s.ResolveWallpaper("~/Pictures/in.png"); err != nil || got != filepath.Join(home, "Pictures", "a.jpg") {
		t.Fatal(got, err)
	}
	for p, want := range map[string]string{
		"~/Pictures/out.png":  "outside",
		"~/Pictures/none.jpg": "not there",
		"~/dir.png":           "not a file",
		"/etc/passwd.png":     "your home",
	} {
		if _, err := s.ResolveWallpaper(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestFileURI(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/Pictures/a.jpg":  "'file:///home/u/Pictures/a.jpg'",
		"/home/u/a b'c\\d%.jpg":   "'file:///home/u/a%20b%27c%5Cd%25.jpg'",
		"/home/u/ç.png":           "'file:///home/u/%C3%A7.png'",
		"/home/u/x';rm -rf ~;'.p": "'file:///home/u/x%27%3Brm%20-rf%20~%3B%27.p'",
	} {
		got := fileURI(in)
		if got != want || safeexec.GSettingsFileURI(got) != nil {
			t.Errorf("%q: %q", in, got)
		}
		if f := (Wallpaper{URI: got}).File(); f != in {
			t.Errorf("round trip %q: %q", in, f)
		}
	}
}

func TestCurrentAndSet(t *testing.T) {
	g := newFake()
	s := New(Config{Desktop: Desktop{"GNOME", true}, Run: g.run, Backlight: t.TempDir()})
	ctx := context.Background()
	st := s.Current(ctx)
	if st.Wallpaper == nil || st.Wallpaper.File() != "/usr/share/backgrounds/gnome/blobs-l.svg" || st.DoNotDisturb == nil || *st.DoNotDisturb || st.Brightness != nil {
		t.Fatalf("%+v", st)
	}
	if err := s.SetDoNotDisturb(ctx, true); err != nil {
		t.Fatal(err)
	}
	if g.values["org.gnome.desktop.notifications show-banners"] != "false" {
		t.Fatal(g.values)
	}
	// Putting back what Current read.
	old := *st.Wallpaper
	g.values["org.gnome.desktop.background picture-uri"] = "'file:///home/u/new.jpg'"
	if err := s.SetWallpaperURIs(ctx, old); err != nil {
		t.Fatal(err)
	}
	if g.values["org.gnome.desktop.background picture-uri"] != old.URI || g.values["org.gnome.desktop.background picture-uri-dark"] != old.DarkURI {
		t.Fatal(g.values)
	}
	if err := s.SetWallpaperURIs(ctx, Wallpaper{URI: "'file:///a' 'b'", DarkURI: old.DarkURI}); err == nil {
		t.Fatal("set a hostile value")
	}
	g.fail = true
	if err := s.SetDoNotDisturb(ctx, false); err == nil {
		t.Fatal("a failed gsettings passed")
	}
	if st := s.Current(ctx); st.Wallpaper != nil && st.Wallpaper.URI != "" {
		t.Fatalf("%+v", st)
	}
}

// Another desktop is detected, read without gsettings and refused by name.
func TestOtherDesktop(t *testing.T) {
	g := newFake()
	s := New(Config{Desktop: Desktop{"KDE", false}, Run: g.run, Backlight: t.TempDir()})
	st := s.Current(context.Background())
	if st.Wallpaper != nil || st.DoNotDisturb != nil || len(g.calls) != 0 {
		t.Fatalf("%+v %v", st, g.calls)
	}
	if err := s.SetDoNotDisturb(context.Background(), true); err == nil {
		t.Fatal("set on KDE")
	}
	reg := actions.NewRegistry(noAudit{})
	s.Register(reg)
	for key, target := range map[string]string{"session/dnd": "on", "session/wallpaper": "~/a.jpg"} {
		a, _ := reg.Lookup(key)
		p, err := a.Plan(context.Background(), actions.Request{Target: target, Params: map[string]string{}})
		if err != nil || len(p.Items) != 0 || len(p.Blocked) != 1 || p.Blocked[0].Reason != ReasonDesktop || p.Blocked[0].Detail != "KDE" {
			t.Errorf("%s: %v %+v", key, err, p)
		}
	}
}

func TestActions(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "w a.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newFake()
	s := New(Config{Desktop: Desktop{"GNOME", true}, Home: home, Run: g.run})
	reg := actions.NewRegistry(noAudit{})
	s.Register(reg)
	ctx := context.Background()
	wp, _ := reg.Lookup("session/wallpaper")
	for _, bad := range []actions.Request{
		{Target: "/etc/shadow", Params: map[string]string{}},
		{Target: "~/w a.jpg", Params: map[string]string{"x": "y"}},
	} {
		if wp.Validate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	r := actions.Request{Target: "~/w a.jpg", Params: map[string]string{}}
	p, err := wp.Plan(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := wp.Run(ctx, r, p); err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	want := fileURI(filepath.Join(home, "w a.jpg"))
	if g.values["org.gnome.desktop.background picture-uri"] != want || g.values["org.gnome.desktop.background picture-uri-dark"] != want {
		t.Fatal(g.values)
	}
	if _, err := wp.Plan(ctx, actions.Request{Target: "~/missing.jpg", Params: map[string]string{}}); err == nil {
		t.Fatal("planned a missing file")
	}
	back, _ := reg.Lookup("session/wallpaper-uris")
	for _, bad := range []actions.Request{
		{Target: "wallpaper", Params: map[string]string{"uri": "'file:///a.jpg'"}},
		{Target: "x", Params: map[string]string{"uri": "'file:///a.jpg'", "dark_uri": "'file:///b.jpg'"}},
		{Target: "wallpaper", Params: map[string]string{"uri": "'file:///a.jpg' x", "dark_uri": "'file:///b.jpg'"}},
	} {
		if back.Validate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	r = actions.Request{Target: "wallpaper", Params: map[string]string{"uri": "'file:///usr/share/backgrounds/l.svg'", "dark_uri": "'file:///usr/share/backgrounds/d.svg'"}}
	p, err = back.Plan(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := back.Run(ctx, r, p); err != nil || res.Done != 1 ||
		g.values["org.gnome.desktop.background picture-uri-dark"] != "'file:///usr/share/backgrounds/d.svg'" {
		t.Fatalf("%v %+v %v", err, res, g.values)
	}
	dnd, _ := reg.Lookup("session/dnd")
	if dnd.Validate(actions.Request{Target: "maybe", Params: map[string]string{}}) == nil {
		t.Fatal("accepted maybe")
	}
	r = actions.Request{Target: "on", Params: map[string]string{}}
	p, _ = dnd.Plan(ctx, r)
	if res, err := dnd.Run(ctx, r, p); err != nil || res.Done != 1 || g.values["org.gnome.desktop.notifications show-banners"] != "false" {
		t.Fatalf("%v %+v %v", err, res, g.values)
	}
	g.fail = true
	if res, err := dnd.Run(ctx, r, p); err != nil || res.Done != 0 || len(res.Failed) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestPreferredBacklight(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, typ, cur, max string) {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{"type": typ, "brightness": cur, "max_brightness": max} {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := New(Config{Backlight: dir})
	if _, ok := s.PreferredBacklight(); ok {
		t.Fatal("found one in an empty folder")
	}
	mk("acpi_video0", "firmware", "5", "10")
	mk("intel_backlight", "raw", "960", "1920")
	mk("broken", "firmware", "x", "10")
	mk("zero", "firmware", "1", "0")
	b, ok := s.PreferredBacklight()
	if !ok || b.Name != "acpi_video0" || b.Percent != 50 {
		t.Fatalf("%+v", b)
	}
	if err := os.RemoveAll(filepath.Join(dir, "acpi_video0")); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.PreferredBacklight(); b.Name != "intel_backlight" || b.Percent != 50 {
		t.Fatalf("%+v", b)
	}
	st := New(Config{Backlight: dir, Desktop: Desktop{"KDE", false}}).Current(context.Background())
	if st.Backlight != "intel_backlight" || st.Brightness == nil || *st.Brightness != 50 {
		t.Fatalf("%+v", st)
	}
}

func TestConnectedDisplays(t *testing.T) {
	sys := t.TempDir()
	for name, status := range map[string]string{
		"card0-eDP-1": "connected", "card0-HDMI-A-1": "disconnected", "card1-DP-2": "connected\n", "card0": "", "renderD128": "connected", "version": "",
	} {
		d := filepath.Join(sys, "class", "drm", name)
		_ = os.MkdirAll(d, 0o700)
		if status != "" {
			_ = os.WriteFile(filepath.Join(d, "status"), []byte(status), 0o600)
		}
	}
	if n, err := ConnectedDisplays(sys); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := ConnectedDisplays(filepath.Join(sys, "none")); err == nil {
		t.Fatal("no error without sysfs")
	}
}

// Gate 10: a wallpaper path that passes stays under its root, and its URI
// is one GVariant string that gsettings-set accepts.
func FuzzCheckWallpaper(f *testing.F) {
	for _, s := range []string{"~/Pictures/a.png", "/usr/share/backgrounds/x/y.jpg", "~/../etc/a.png", "~/a b'c\\d.png", "~//a.png", "~/a/./b.svg", "/usr/share/wallpapers/%2e%2e/a.webp"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if CheckWallpaper(p) != nil {
			return
		}
		rest := ""
		for _, r := range WallpaperRoots {
			if strings.HasPrefix(p, r) {
				rest = strings.TrimPrefix(p, r)
				break
			}
		}
		if rest == "" || strings.HasPrefix(rest, "/") || rest == ".." || strings.HasPrefix(rest, "../") || strings.Contains(rest, "/../") || strings.HasSuffix(rest, "/..") {
			t.Fatalf("accepted %q", p)
		}
		if err := safeexec.GSettingsFileURI(fileURI(filepath.Join("/home/u", rest))); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
	})
}

// Whatever path it is given, fileURI makes one quoted string with nothing
// gsettings would parse as more than a string.
func FuzzFileURI(f *testing.F) {
	f.Add("/home/u/a b'c\\d\n.png")
	f.Fuzz(func(t *testing.T, p string) {
		u := fileURI("/" + strings.TrimLeft(p, "/"))
		if strings.Count(u, "'") != 2 || strings.ContainsAny(u, "\\ \n\t\"") {
			t.Fatalf("%q -> %q", p, u)
		}
	})
}
