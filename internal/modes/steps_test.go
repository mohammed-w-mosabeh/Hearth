package modes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/session"
)

func TestSessionSteps(t *testing.T) {
	m, err := Parse("work", []byte(work+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m.Session.Disconnect = []string{"11111111-2222-3333-4444-555555555555"}
	got, _ := json.Marshal(m.SessionSteps("intel_backlight"))
	want := `[{"action":"power/profile","target":"balanced","params":{}},` +
		`{"action":"system/brightness","target":"intel_backlight","params":{"percent":"70"}},` +
		`{"action":"session/dnd","target":"on","params":{}},` +
		`{"action":"session/wallpaper","target":"~/Pictures/desk.jpg","params":{}},` +
		`{"action":"network/disconnect","target":"11111111-2222-3333-4444-555555555555","params":{}},` +
		`{"action":"network/connect","target":"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11","params":{}}]`
	if string(got) != want {
		t.Fatalf("%s", got)
	}
	// No backlight: brightness is left out. Apps are not session steps.
	for _, s := range m.SessionSteps("") {
		if s.Action == "system/brightness" || s.Action == "app/launch" {
			t.Fatalf("%+v", s)
		}
	}
	quiet, _ := Parse("quiet", []byte("name = \"Q\"\n[session]\ndo_not_disturb = false\n"))
	if s := quiet.SessionSteps("x"); len(s) != 1 || s[0].Target != "off" {
		t.Fatalf("%+v", s)
	}
}

// fakeGSettings answers gsettings get from values, after the real
// allowlist's check.
func fakeGSettings(values map[string]string) session.Runner {
	return func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if err := safeexec.Check(name, args...); err != nil {
			return safeexec.Result{}, err
		}
		key := args[1] + " " + args[2]
		if args[0] == "set" {
			values[key] = args[3]
			return safeexec.Result{}, nil
		}
		return safeexec.Result{Stdout: []byte(values[key] + "\n")}, nil
	}
}

func TestSessionSettings(t *testing.T) {
	dir := t.TempDir()
	bl := filepath.Join(dir, "intel_backlight")
	_ = os.MkdirAll(bl, 0o700)
	for f, v := range map[string]string{"type": "raw", "brightness": "480", "max_brightness": "960"} {
		_ = os.WriteFile(filepath.Join(bl, f), []byte(v), 0o600)
	}
	values := map[string]string{
		"org.gnome.desktop.background picture-uri":      "'file:///usr/share/backgrounds/l.svg'",
		"org.gnome.desktop.background picture-uri-dark": "'file:///usr/share/backgrounds/d.svg'",
		"org.gnome.desktop.notifications show-banners":  "true",
	}
	ss := SessionSettings{
		Session: session.New(session.Config{Desktop: session.Desktop{Name: "GNOME", GNOME: true}, Run: fakeGSettings(values), Backlight: dir}),
		Power:   func() (string, bool) { return "balanced", true },
	}
	ctx := context.Background()
	for name, want := range map[string]string{
		SettingPower: "balanced", SettingBrightness: "50", SettingDND: "off",
		SettingWallpaper: "'file:///usr/share/backgrounds/l.svg'|'file:///usr/share/backgrounds/d.svg'",
	} {
		if got, err := ss.Current(ctx, name); err != nil || got != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	// A wallpaper that could not be put back exactly is not touched.
	values["org.gnome.desktop.background picture-uri"] = "'file:///home/u/a b.jpg'"
	if _, err := ss.Current(ctx, SettingWallpaper); err == nil {
		t.Fatal("read a wallpaper it cannot put back")
	}
	if _, err := ss.Current(ctx, "volume"); err == nil {
		t.Fatal("read an unknown setting")
	}
	for _, c := range []struct{ name, value, key, target string }{
		{SettingBrightness, "0", "system/brightness", "intel_backlight"},
		{SettingDND, "on", "session/dnd", "on"},
		{SettingWallpaper, "~/a.jpg", "session/wallpaper", "~/a.jpg"},
		{SettingWallpaper, "'file:///l.jpg'|'file:///d.jpg'", "session/wallpaper-uris", "wallpaper"},
		{SettingPower, "power-saver", "power/profile", "power-saver"},
	} {
		key, r, err := ss.Request(c.name, c.value)
		if err != nil || key != c.key || r.Target != c.target {
			t.Errorf("%s=%s: %s %+v %v", c.name, c.value, key, r, err)
		}
		if c.name == SettingBrightness && r.Params["percent"] != "1" {
			t.Errorf("brightness 0 became %q, not the lowest that keeps the screen on", r.Params["percent"])
		}
	}
	// Without a power daemon there is nothing to read or put back.
	ss.Power = func() (string, bool) { return "", false }
	if _, err := ss.Current(ctx, SettingPower); err == nil {
		t.Fatal("read a power profile without the daemon")
	}
}
