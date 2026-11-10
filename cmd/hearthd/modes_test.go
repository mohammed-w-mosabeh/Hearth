package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Every action a mode's steps name is registered by the real wiring, and
// accepts what a valid mode file holds (ROADMAP 10.3).
func TestModeStepsAreRegistered(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+t.TempDir()+"/no-bus")
	p := testPaths(t)
	if err := writeConfig(p, Config{Port: pickPort(context.Background())}); err != nil {
		t.Fatal(err)
	}
	got := make(chan *actions.Registry, 1)
	registryHook = func(r *actions.Registry) { got <- r }
	t.Cleanup(func() { registryHook = nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, p, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, string) error { return nil })
	}()
	var reg *actions.Registry
	select {
	case reg = <-got:
	case err := <-done:
		t.Fatalf("run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("hearthd did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m, err := modes.Parse("all", []byte(`name = "All"
[session]
wallpaper = "~/Pictures/a.jpg"
do_not_disturb = true
brightness = 40
power_profile = "power-saver"
open = ["org.gnome.TextEditor"]
connect = ["0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"]
disconnect = ["11111111-2222-3333-4444-555555555555"]
`))
	if err != nil {
		t.Fatal(err)
	}
	steps := m.SessionSteps("intel_backlight")
	if len(steps) != 6 {
		t.Fatalf("%+v", steps)
	}
	for _, s := range steps {
		a, ok := reg.Lookup(s.Action)
		if !ok {
			t.Errorf("%s is not registered", s.Action)
			continue
		}
		if err := a.Validate(actions.Request{Target: s.Target, Params: s.Params}); err != nil {
			t.Errorf("%s refuses %q: %v", s.Action, s.Target, err)
		}
	}
	// Putting a wallpaper back, as revert does.
	back, err := modes.StepFor(modes.Item{Kind: modes.KindSetting, Target: modes.SettingWallpaper,
		Do: "'file:///usr/share/backgrounds/l.svg'|'file:///usr/share/backgrounds/d.svg'"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := reg.Lookup(back.Action); !ok || a.Validate(actions.Request{Target: back.Target, Params: back.Params}) != nil {
		t.Errorf("%s does not accept %+v", back.Action, back)
	}
	for _, key := range []string{"app/launch", "unit/start", "unit/stop", "container/start", "container/stop", "project/start", "project/stop", "mode/arm", "mode/disarm", "mode/approve", "mode/dismiss", "stack/grant", "stack/revoke-grant"} {
		if _, ok := reg.Lookup(key); !ok {
			t.Errorf("%s is not registered", key)
		}
	}
}

// Profiles in the database become mode files in hearthd's own config folder
// when it starts (ROADMAP 10.2), and keep their IDs.
func TestRunMigratesProfiles(t *testing.T) {
	p := testPaths(t)
	if err := writeConfig(p, Config{Port: pickPort(context.Background())}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), p.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.AddProfile(context.Background(), store.Profile{Name: "Focus Time", Items: `[{"kind":"app","target":"org.gnome.TextEditor","do":"start"}]`})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, p, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, string) error { return nil })
	}()
	deadline := time.Now().Add(5 * time.Second)
	for checkHealth(context.Background(), cfg.Port) != nil {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("hearthd never became healthy: %v", <-done)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(p.ConfigDir, "modes", "focus-time.toml")) // #nosec G304 -- a test's temporary folder
	if err != nil || !strings.Contains(string(raw), `name = "Focus Time"`) || !strings.Contains(string(raw), "org.gnome.TextEditor") {
		t.Fatalf("%v\n%s", err, raw)
	}
	db, err = store.Open(context.Background(), p.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Profile(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("the profile is still in the database:", err)
	}
	if nums, _ := db.ModeNumbers(context.Background(), nil); nums["focus-time"] != id {
		t.Fatalf("%v", nums)
	}
}
