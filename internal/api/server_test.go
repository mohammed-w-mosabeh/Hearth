package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
)

type harness struct {
	s    *Server
	base string // http://127.0.0.1:<port>
	key  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	am, err := auth.NewManager(filepath.Join(t.TempDir(), "token"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Auth:   am,
		Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Hearth</title>")}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.API.HandleFunc("POST /api/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = w.Write(b)
	})
	s.API.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, "pong")
	})
	if err := s.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	tok, err := os.ReadFile(am.TokenPath())
	if err != nil {
		t.Fatal(err)
	}
	key, err := am.Exchange(strings.TrimSpace(string(tok)))
	if err != nil {
		t.Fatal(err)
	}
	return &harness{s: s, base: fmt.Sprintf("http://127.0.0.1:%d", s.Port()), key: key}
}

func (h *harness) origin() string { return h.base }

type req struct {
	method, path string
	host         string // overrides Host header
	origin       string
	key          string
	body         string
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       5 * time.Second,
}

// result is a fully read and closed response.
type result struct {
	code int
	hdr  http.Header
	body string
}

func (h *harness) do(t *testing.T, r req) result {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	hr, err := http.NewRequestWithContext(context.Background(), r.method, h.base+r.path, strings.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	if r.host != "" {
		hr.Host = r.host
	}
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	if r.key != "" {
		hr.Header.Set("Authorization", "Bearer "+r.key)
	}
	return send(t, hr)
}

func send(t *testing.T, hr *http.Request) result {
	t.Helper()
	resp, err := noRedirect.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{code: resp.StatusCode, hdr: resp.Header, body: string(b)}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	h := newHarness(t)
	addr, ok := h.s.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v", h.s.Addr())
	}
}

func TestShutdownForcesUnusedConnections(t *testing.T) {
	h := newHarness(t)
	// A connection that never sends a request, like a browser preconnect.
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "tcp", h.s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := h.s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}

func TestHealthIsPublic(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, req{path: "/api/v1/health"})
	if resp.code != 200 || strings.TrimSpace(resp.body) != `{"status":"ok"}` {
		t.Fatalf("%d %s", resp.code, resp.body)
	}
}

func TestHostCheck(t *testing.T) {
	h := newHarness(t)
	port := h.s.Port()
	for _, host := range []string{
		"evil.test", // DNS rebinding
		fmt.Sprintf("evil.test:%d", port),
		"127.0.0.1", // missing port
		fmt.Sprintf("127.0.0.1:%d", port+1),
		fmt.Sprintf("0.0.0.0:%d", port),
		fmt.Sprintf("[::1]:%d", port),
		fmt.Sprintf("LOCALHOST:%d", port),
		fmt.Sprintf("localhost.:%d", port),
		fmt.Sprintf("127.0.0.1:%d.evil.test", port),
	} {
		for _, path := range []string{"/api/v1/health", "/", "/auth?t=x", "/api/v1/ping"} {
			if resp := h.do(t, req{path: path, host: host, key: h.key}); resp.code != http.StatusForbidden {
				t.Errorf("Host %q %s: status %d, want 403", host, path, resp.code)
			}
		}
	}
	for _, host := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port)} {
		if resp := h.do(t, req{path: "/api/v1/health", host: host}); resp.code != 200 {
			t.Errorf("Host %q: status %d, want 200", host, resp.code)
		}
	}
}

func TestOriginCheckOnStateChanging(t *testing.T) {
	h := newHarness(t)
	port := h.s.Port()
	for _, origin := range []string{
		"", // missing
		"null",
		"http://evil.test",
		fmt.Sprintf("http://127.0.0.1:%d.evil.test", port),
		fmt.Sprintf("https://127.0.0.1:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port+1),
		fmt.Sprintf("http://127.0.0.1:%d/", port),
	} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			resp := h.do(t, req{method: m, path: "/api/v1/echo", origin: origin, key: h.key, body: "x"})
			if resp.code != http.StatusForbidden {
				t.Errorf("%s Origin %q: status %d, want 403", m, origin, resp.code)
			}
		}
	}
	for _, origin := range []string{h.origin(), fmt.Sprintf("http://localhost:%d", port)} {
		resp := h.do(t, req{method: http.MethodPost, path: "/api/v1/echo", origin: origin, key: h.key, body: "x"})
		if resp.code != 200 {
			t.Errorf("Origin %q: status %d, want 200", origin, resp.code)
		}
	}
}

func TestAuthRequired(t *testing.T) {
	h := newHarness(t)
	cases := []req{
		{path: "/api/v1/ping"},
		{path: "/api/v1/ping", key: "wrong"},
		{path: "/api/v1/does-not-exist"},
		{method: http.MethodPost, path: "/api/v1/echo", origin: h.origin(), body: "x"},
		{method: http.MethodPost, path: "/api/v1/echo", origin: h.origin(), key: "wrong", body: "x"},
		{path: "/api/v1/ws"},
	}
	for _, c := range cases {
		if resp := h.do(t, c); resp.code != http.StatusUnauthorized {
			t.Errorf("%s %s key=%q: status %d, want 401", c.method, c.path, c.key, resp.code)
		}
	}
	if resp := h.do(t, req{path: "/api/v1/ping", key: h.key}); resp.code != 200 {
		t.Errorf("valid key: status %d", resp.code)
	}
	if resp := h.do(t, req{path: "/api/v1/session"}); resp.code != http.StatusUnauthorized {
		t.Errorf("session without key: status %d", resp.code)
	}
	if resp := h.do(t, req{path: "/api/v1/session", key: h.key}); resp.code != 200 {
		t.Errorf("session with key: status %d", resp.code)
	}
}

func TestAuthExchangeViaServer(t *testing.T) {
	h := newHarness(t)
	tok, _ := os.ReadFile(h.s.cfg.Auth.TokenPath())
	resp := h.do(t, req{path: "/auth?t=" + strings.TrimSpace(string(tok))})
	if resp.code != http.StatusSeeOther || !strings.HasPrefix(resp.hdr.Get("Location"), "/#k=") {
		t.Fatalf("status %d, Location %q", resp.code, resp.hdr.Get("Location"))
	}
	if resp := h.do(t, req{path: "/auth?t=" + strings.TrimSpace(string(tok))}); resp.code != http.StatusForbidden {
		t.Fatalf("reused token: status %d", resp.code)
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	h := newHarness(t)
	want := map[string]string{
		"Content-Security-Policy":      csp,
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for _, r := range []req{
		{path: "/"},
		{path: "/api/v1/health"},
		{path: "/api/v1/ping"},                          // 401
		{path: "/auth?t=nope"},                          // 403
		{path: "/", host: "evil.test"},                  // 403 host
		{method: http.MethodPost, path: "/api/v1/echo"}, // 403 origin
		{path: "/missing"},                              // 404
	} {
		resp := h.do(t, r)
		for k, v := range want {
			if got := resp.hdr.Get(k); got != v {
				t.Errorf("%s %s (%d): %s = %q", r.method, r.path, resp.code, k, got)
			}
		}
		if c := resp.hdr.Values("Set-Cookie"); len(c) != 0 {
			t.Errorf("%s: Set-Cookie %v", r.path, c)
		}
	}
	if strings.Contains(csp, "unsafe-") {
		t.Fatal("CSP contains unsafe-*")
	}
	if resp := h.do(t, req{path: "/api/v1/ping", key: h.key}); resp.hdr.Get("Cache-Control") != "no-store" {
		t.Error("API response cacheable")
	}
}

func TestStaticRejectsUnsafeMethods(t *testing.T) {
	h := newHarness(t)
	if resp := h.do(t, req{path: "/"}); resp.code != 200 {
		t.Fatalf("GET /: %d", resp.code)
	}
	if resp := h.do(t, req{method: http.MethodPost, path: "/", origin: h.origin()}); resp.code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %d", resp.code)
	}
}

func TestBodyLimit(t *testing.T) {
	h := newHarness(t)
	big := strings.Repeat("a", maxBody+1)
	resp := h.do(t, req{method: http.MethodPost, path: "/api/v1/echo", origin: h.origin(), key: h.key, body: big})
	if resp.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.code)
	}

	// Chunked body with no Content-Length is cut by MaxBytesReader.
	hr, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, h.base+"/api/v1/echo",
		io.MultiReader(strings.NewReader(big)))
	hr.ContentLength = -1
	hr.Header.Set("Origin", h.origin())
	hr.Header.Set("Authorization", "Bearer "+h.key)
	if resp := send(t, hr); resp.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: status %d, want 413", resp.code)
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t)
	limited := 0
	for range 60 {
		resp := h.do(t, req{method: http.MethodPost, path: "/api/v1/echo", origin: h.origin(), key: h.key, body: "x"})
		if resp.code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("60 rapid POSTs were never rate limited")
	}
	// Reads are not limited.
	for range 60 {
		if resp := h.do(t, req{path: "/api/v1/ping", key: h.key}); resp.code != 200 {
			t.Fatalf("GET limited: %d", resp.code)
		}
	}
}

func TestBucket(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBucket(20, 40)
	b.now = func() time.Time { return now }
	for i := range 40 {
		if !b.allow() {
			t.Fatalf("burst denied at %d", i)
		}
	}
	if b.allow() {
		t.Fatal("allowed beyond burst")
	}
	now = now.Add(100 * time.Millisecond) // +2 tokens
	first, second, third := b.allow(), b.allow(), b.allow()
	if !first || !second || third {
		t.Fatal("refill not 20/s")
	}
}

// dialWS returns the connection (nil on failure), the handshake status, and
// the subprotocol the server echoed.
func (h *harness) dialWS(t *testing.T, origin string, protos ...string) (*websocket.Conn, int, string, error) {
	t.Helper()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.base, "http")+"/api/v1/ws", &websocket.DialOptions{
		HTTPHeader: hdr, Subprotocols: protos,
	})
	if resp == nil {
		return conn, 0, "", err
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	return conn, resp.StatusCode, resp.Header.Get("Sec-WebSocket-Protocol"), err
}

func TestWebSocketRejections(t *testing.T) {
	h := newHarness(t)
	keyProto := "hearth.key." + h.key
	cases := []struct {
		name   string
		origin string
		protos []string
		want   int
	}{
		{"foreign origin", "http://evil.test", []string{auth.Subprotocol, keyProto}, 403},
		{"missing origin", "", []string{auth.Subprotocol, keyProto}, 403},
		{"null origin", "null", []string{auth.Subprotocol, keyProto}, 403},
		{"no key", h.origin(), []string{auth.Subprotocol}, 401},
		{"wrong key", h.origin(), []string{auth.Subprotocol, "hearth.key.nope"}, 401},
	}
	for _, c := range cases {
		conn, code, _, err := h.dialWS(t, c.origin, c.protos...)
		if err == nil {
			_ = conn.CloseNow()
			t.Errorf("%s: connected", c.name)
			continue
		}
		if code != c.want {
			t.Errorf("%s: status %d, want %d (%v)", c.name, code, c.want, err)
		}
	}

	// Valid key but without the hearth.v1 subprotocol: closed with policy violation.
	conn, _, _, err := h.dialWS(t, h.origin(), keyProto)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("got %v, want policy violation close", err)
	}
}

func TestWebSocketSubscribePublish(t *testing.T) {
	h := newHarness(t)
	watch := h.s.Hub.Watch("metrics")
	conn, _, got, err := h.dialWS(t, h.origin(), auth.Subprotocol, "hearth.key."+h.key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if got != auth.Subprotocol {
		t.Fatalf("server echoed subprotocol %q; must never echo the key", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":"sub","topic":"metrics"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.s.Hub.Subscribers("metrics") == 1 })
	select {
	case <-watch:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch not signalled on subscribe")
	}

	if err := h.s.Hub.Publish("other", 1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Hub.Publish("metrics", map[string]int{"cpu": 7}); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m serverMsg
	if err := json.Unmarshal(data, &m); err != nil || m.Topic != "metrics" || string(m.Data) != `{"cpu":7}` {
		t.Fatalf("got %s (%v)", data, err)
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":"unsub","topic":"metrics"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.s.Hub.Subscribers("metrics") == 0 })
	select {
	case <-watch:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch not signalled on unsubscribe")
	}
}

func TestWebSocketBadMessages(t *testing.T) {
	h := newHarness(t)
	for _, msg := range []string{
		`not json`,
		`{"op":"exec","topic":"metrics"}`,
		`{"op":"sub","topic":"Bad Topic"}`,
		`{"op":"sub","topic":"` + strings.Repeat("a", wsReadLimit) + `"}`,
	} {
		conn, _, _, err := h.dialWS(t, h.origin(), auth.Subprotocol, "hearth.key."+h.key)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Write(ctx, websocket.MessageText, []byte(msg))
		_, _, err = conn.Read(ctx)
		cancel()
		_ = conn.CloseNow()
		if s := websocket.CloseStatus(err); s != websocket.StatusPolicyViolation && s != websocket.StatusMessageTooBig {
			t.Errorf("%.40q: got %v, want policy/too-big close", msg, err)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
