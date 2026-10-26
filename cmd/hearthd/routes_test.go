package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/api"
)

// TestEveryRouteIsGuarded boots the real wiring and checks every API route
// hearthd registers (SECURITY checklist): without the session key it gets
// 401; a foreign Origin (state-changing routes) or Host gets 403; no
// response sets a cookie. A route added outside the authenticated mux, or
// a middleware dropped, fails here.
func TestEveryRouteIsGuarded(t *testing.T) {
	// No session bus: nothing here may notify the desktop or reach the
	// user's systemd.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+t.TempDir()+"/no-bus")
	p := testPaths(t)
	port := pickPort(context.Background())
	if err := writeConfig(p, Config{Port: port}); err != nil {
		t.Fatal(err)
	}
	got := make(chan *api.Server, 1)
	serverHook = func(s *api.Server) { got <- s }
	t.Cleanup(func() { serverHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, p, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, string) error { return nil })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	var srv *api.Server
	select {
	case srv = <-got:
	case err := <-done:
		t.Fatalf("run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("hearthd did not start")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	for deadline := time.Now().Add(5 * time.Second); checkHealth(context.Background(), port) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("not healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A real session key, through the launch token exchange.
	tok, err := os.ReadFile(p.TokenFile())
	if err != nil {
		t.Fatal(err)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(base + "/auth?t=" + strings.TrimSpace(string(tok)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	key := strings.TrimPrefix(resp.Header.Get("Location"), "/#k=")
	if resp.StatusCode != http.StatusSeeOther || key == "" {
		t.Fatalf("auth exchange: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	param := regexp.MustCompile(`\{[^}]+\}`)
	do := func(method, path, host, origin, bearer string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if c := resp.Header.Values("Set-Cookie"); len(c) > 0 {
			t.Errorf("%s %s set a cookie: %v", method, path, c)
		}
		time.Sleep(60 * time.Millisecond) // stay under the rate limit (20/s)
		return resp.StatusCode
	}
	self := "127.0.0.1:" + strconv.Itoa(port)
	good := "http://" + self
	changing := 0
	for _, pat := range srv.API.Patterns() {
		method, path, ok := strings.Cut(pat, " ")
		if !ok || !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("route %q is not a method + /api/v1 path", pat)
			continue
		}
		path = param.ReplaceAllString(path, "1")
		if code := do(method, path, "", good, ""); code != http.StatusUnauthorized {
			t.Errorf("%s without a key: %d, want 401", pat, code)
		}
		if code := do(method, path, "evil.test", good, key); code != http.StatusForbidden {
			t.Errorf("%s with Host evil.test: %d, want 403", pat, code)
		}
		if method != http.MethodGet {
			changing++
			if code := do(method, path, "", "http://evil.test", key); code != http.StatusForbidden {
				t.Errorf("%s with a foreign Origin: %d, want 403", pat, code)
			}
			if code := do(method, path, "", "", key); code != http.StatusForbidden {
				t.Errorf("%s without Origin: %d, want 403", pat, code)
			}
		}
	}
	// All typed actions share POST /api/v1/actions/{kind}/{action}.
	if changing < 6 {
		t.Fatalf("only %d state-changing routes found: %v", changing, srv.API.Patterns())
	}
	t.Logf("%d routes, %d state-changing, all guarded", len(srv.API.Patterns()), changing)
}
