package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRefuseRoot(t *testing.T) {
	if refuseRoot(0) == nil {
		t.Fatal("euid 0 accepted")
	}
	if err := refuseRoot(1000); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePaths(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	p, err := resolvePaths(env(map[string]string{"HOME": "/home/u"}))
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigDir != "/home/u/.config/hearth" || p.DataDir != "/home/u/.local/share/hearth" {
		t.Fatalf("defaults: %+v", p)
	}

	p, _ = resolvePaths(env(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "/cfg/", "XDG_DATA_HOME": "/data"}))
	if p.ConfigDir != "/cfg/hearth" || p.DataDir != "/data/hearth" || p.DBFile() != "/data/hearth/hearth.db" {
		t.Fatalf("xdg: %+v", p)
	}

	// Relative XDG values are invalid per the spec and ignored.
	p, _ = resolvePaths(env(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "rel", "XDG_DATA_HOME": "../x"}))
	if p.ConfigDir != "/home/u/.config/hearth" || p.DataDir != "/home/u/.local/share/hearth" {
		t.Fatalf("relative: %+v", p)
	}

	if _, err := resolvePaths(env(map[string]string{})); err == nil {
		t.Fatal("accepted empty HOME")
	}
}

func TestParseConfig(t *testing.T) {
	good, err := parseConfig([]byte(`{"port": 7380}`))
	if err != nil || good.Port != 7380 {
		t.Fatalf("%+v %v", good, err)
	}
	for _, bad := range []string{
		``, `{}`, `{"port": 80}`, `{"port": 70000}`, `{"port": "7380"}`,
		`{"port": 7380, "listen": "0.0.0.0"}`, `{"port": 7380}{"port": 1}`, `[]`,
	} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{ConfigDir: filepath.Join(dir, "config", "hearth"), DataDir: filepath.Join(dir, "data", "hearth")}
}

func TestLoadConfigFirstRun(t *testing.T) {
	p := testPaths(t)
	cfg, first, err := loadConfig(context.Background(), p)
	if err != nil || !first || cfg.Port < 1024 {
		t.Fatalf("%+v %v %v", cfg, first, err)
	}
	fi, err := os.Stat(p.ConfigFile())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v %v", fi, err)
	}
	again, first, err := loadConfig(context.Background(), p)
	if err != nil || first || again.Port != cfg.Port {
		t.Fatalf("second load: %+v %v %v", again, first, err)
	}
}

func TestRunServesAndStops(t *testing.T) {
	p := testPaths(t)
	// Fix the port so the test knows it without parsing logs.
	if err := writeConfig(p, Config{Port: pickPort(context.Background())}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.ConfigFile())
	var cfg Config
	_ = json.Unmarshal(b, &cfg)

	ctx, cancel := context.WithCancel(context.Background())
	opened := make(chan string, 1)
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		done <- run(ctx, p, log, func(_ context.Context, u string) error { opened <- u; return nil })
	}()

	base := "http://127.0.0.1:" + strconv.Itoa(cfg.Port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := checkHealth(context.Background(), cfg.Port); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("hearthd never became healthy: %v", <-done)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// UI is served from the embedded build.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `<div id="app">`) {
		t.Fatalf("GET /: %d %.80s", resp.StatusCode, body)
	}

	// Not a first run (config existed): no browser.
	select {
	case u := <-opened:
		t.Fatalf("opened %s on a non-first run", u)
	default:
	}

	for _, f := range []string{p.TokenFile(), p.DBFile()} {
		fi, err := os.Stat(f)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestRunFirstRunOpensBrowser(t *testing.T) {
	p := testPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, p, slog.New(slog.NewTextHandler(io.Discard, nil)),
			func(_ context.Context, u string) error { opened <- u; return nil })
	}()
	select {
	case u := <-opened:
		tok, _ := os.ReadFile(p.TokenFile())
		if !strings.HasPrefix(u, "http://127.0.0.1:") || !strings.HasSuffix(u, "/auth?t="+strings.TrimSpace(string(tok))) {
			t.Fatalf("launch URL %q", u)
		}
	case err := <-done:
		t.Fatalf("run exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("browser not opened on first run")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
