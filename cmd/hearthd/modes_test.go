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

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

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
