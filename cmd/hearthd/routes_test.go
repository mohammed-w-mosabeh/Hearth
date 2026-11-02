package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	upgrade := false
	do := func(method, path, host, origin, bearer string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if upgrade {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
		}
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
	// Any GET route can be asked to upgrade, whether or not it speaks
	// WebSocket; a foreign or missing Origin never reaches the handler.
	upgrade = true
	for _, pat := range append(srv.API.Patterns(), "GET /api/v1/ws") {
		method, path, _ := strings.Cut(pat, " ")
		if method != http.MethodGet {
			continue
		}
		path = param.ReplaceAllString(path, "1")
		for _, origin := range []string{"http://evil.test", ""} {
			if code := do(method, path, "", origin, key); code != http.StatusForbidden {
				t.Errorf("%s upgrade with Origin %q: %d, want 403", pat, origin, code)
			}
		}
	}
	upgrade = false

	// A read-only token, made as the Tokens page makes it (SECURITY S2a).
	if code := do(http.MethodPost, "/api/v1/tokens", "", good, ""); code != http.StatusUnauthorized {
		t.Errorf("making a token without a session: %d, want 401", code)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/v1/tokens", strings.NewReader(`{"name":"cli"}`))
	req.Header.Set("Origin", good)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("make a token: %d", resp.StatusCode)
	}
	raw, err := os.ReadFile(filepath.Join(p.TokensDir(), "cli.token"))
	if err != nil {
		t.Fatal(err)
	}
	ro := strings.TrimSpace(string(raw))

	// Every route, every method it has, with a good Origin so that only the
	// token's limits can refuse it: a route is open to the token only if it
	// is on the closed list, and then only by GET.
	seen := map[string]bool{}
	for _, pat := range srv.API.Patterns() {
		method, path, _ := strings.Cut(pat, " ")
		path = param.ReplaceAllString(path, "1")
		code := do(method, path, "", good, ro)
		if method == http.MethodGet && api.ReadOnlyRoutes[path] {
			seen[path] = true
			if code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Errorf("%s is listed for tokens but answered %d", pat, code)
			}
		} else if code != http.StatusForbidden {
			t.Errorf("%s with a read-only token: %d, want 403", pat, code)
		}
		// HEAD reaches GET handlers in the mux, so it has to be refused too.
		if method == http.MethodGet {
			if code := do(http.MethodHead, path, "", good, ro); code != http.StatusForbidden {
				t.Errorf("HEAD %s with a read-only token: %d, want 403", path, code)
			}
		}
	}
	for path := range api.ReadOnlyRoutes {
		if !seen[path] {
			t.Errorf("%s is on the read-only list but is no route", path)
		}
	}
	// No WebSocket: not the hub, and no route asked to upgrade, listed or not.
	upgrade = true
	for _, pat := range append(srv.API.Patterns(), "GET /api/v1/ws") {
		method, path, _ := strings.Cut(pat, " ")
		if method != http.MethodGet {
			continue
		}
		path = param.ReplaceAllString(path, "1")
		code := do(method, path, "", good, ro)
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("%s upgrade with a read-only token: %d", pat, code)
		}
	}
	upgrade = false

	// /metrics is off until asked for, and a session key is not a token.
	if code := do(http.MethodGet, "/metrics", "", "", ro); code != http.StatusNotFound {
		t.Errorf("/metrics while off: %d, want 404", code)
	}
	if code := do(http.MethodPut, "/api/v1/tokens/metrics", "", good, ro); code != http.StatusForbidden {
		t.Errorf("a token turned /metrics on: %d", code)
	}
	if code := do(http.MethodGet, "/metrics", "", "", key); code != http.StatusUnauthorized {
		t.Errorf("/metrics with a session key: %d, want 401", code)
	}
	if code := do(http.MethodGet, "/metrics", "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("/metrics without a token: %d, want 401", code)
	}
	if code := do(http.MethodPost, "/metrics", "", good, ro); code == http.StatusOK {
		t.Errorf("POST /metrics: %d", code)
	}
	// Revoking ends it at once.
	if code := do(http.MethodDelete, "/api/v1/tokens/cli", "", good, key); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code := do(http.MethodGet, "/api/v1/session", "", "", ro); code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d, want 401", code)
	}

	// All typed actions share POST /api/v1/actions/{kind}/{action}.
	if changing < 6 {
		t.Fatalf("only %d state-changing routes found: %v", changing, srv.API.Patterns())
	}
	t.Logf("%d routes, %d state-changing, all guarded", len(srv.API.Patterns()), changing)
}
