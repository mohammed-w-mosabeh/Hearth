package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
)

func tokenServer(t *testing.T) (*Server, http.Handler, string, *auth.Tokens) {
	t.Helper()
	dir := t.TempDir()
	am, err := auth.NewManager(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	tk, _, err := auth.NewTokens(filepath.Join(dir, "tokens"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Auth: am, Tokens: tk, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.port.Store(7380)
	info, err := tk.Create("cli")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(info.File)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.handler(), strings.TrimSpace(string(b)), tk
}

func tokenGet(h http.Handler, path, bearer string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "127.0.0.1:7380"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReadOnlyTokenStaysOnItsRoutes(t *testing.T) {
	s, h, tok, _ := tokenServer(t)
	reached := 0
	ok := func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusOK) }
	s.API.HandleFunc("GET /api/v1/net/sockets", ok)
	s.API.HandleFunc("GET /api/v1/terminal", ok)
	s.API.HandleFunc("GET /api/v1/net/{x}", ok)

	if c := tokenGet(h, "/api/v1/net/sockets", tok, nil).Code; c != http.StatusOK {
		t.Errorf("listed route: %d", c)
	}
	if reached != 1 {
		t.Fatalf("handler ran %d times", reached)
	}
	reached = 0
	for _, path := range []string{"/api/v1/terminal", "/api/v1/net/other", "/api/v1/tokens", "/api/v1/net/sockets/"} {
		if c := tokenGet(h, path, tok, nil).Code; c != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", path, c)
		}
	}
	// A listed path is not a way into a WebSocket.
	if c := tokenGet(h, "/api/v1/net/sockets", tok, map[string]string{"Upgrade": "websocket", "Origin": "http://127.0.0.1:7380"}).Code; c != http.StatusForbidden {
		t.Errorf("upgrade: %d, want 403", c)
	}
	if reached != 0 {
		t.Errorf("a refused request reached a handler %d times", reached)
	}
	// An unknown bearer is simply unauthenticated.
	if c := tokenGet(h, "/api/v1/net/sockets", "hearth_ro_nope", nil).Code; c != http.StatusUnauthorized {
		t.Errorf("unknown token: %d, want 401", c)
	}
}

func TestRevokedTokenIsRefusedAtOnce(t *testing.T) {
	s, h, tok, tk := tokenServer(t)
	s.API.HandleFunc("GET /api/v1/net/sockets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := tk.Revoke("cli"); err != nil {
		t.Fatal(err)
	}
	if c := tokenGet(h, "/api/v1/net/sockets", tok, nil).Code; c != http.StatusUnauthorized {
		t.Errorf("revoked: %d, want 401", c)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s, h, tok, _ := tokenServer(t)
	if c := tokenGet(h, "/metrics", tok, nil).Code; c != http.StatusNotFound {
		t.Errorf("not wired: %d", c)
	}
	on := false
	s.SetMetrics(func(context.Context) ([]byte, bool) { return []byte("# EOF\n"), on })
	if c := tokenGet(h, "/metrics", "", nil).Code; c != http.StatusUnauthorized {
		t.Errorf("no token: %d", c)
	}
	if c := tokenGet(h, "/metrics", tok, nil).Code; c != http.StatusNotFound {
		t.Errorf("off: %d", c)
	}
	on = true
	w := tokenGet(h, "/metrics", tok, nil)
	if w.Code != http.StatusOK || w.Body.String() != "# EOF\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/openmetrics-text") {
		t.Errorf("on: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if c := tokenGet(h, "/metrics", tok, map[string]string{"Upgrade": "websocket", "Origin": "http://127.0.0.1:7380"}).Code; c != http.StatusForbidden {
		t.Errorf("upgrade: %d", c)
	}
	// Scraping is a token's job only; a stranger's Host is refused first.
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Host = "evil.test"
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign Host: %d", rec.Code)
	}
}

func TestTokenRoutes(t *testing.T) {
	s, _, _, tk := tokenServer(t)
	st := &memSettings{m: map[string]json.RawMessage{}}
	var audited []string
	fail := false
	s.RegisterTokens(st, func(_ context.Context, action, target, result string) error {
		if fail {
			return io.ErrClosedPipe
		}
		audited = append(audited, action+" "+target+" "+result)
		return nil
	})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.API.ServeHTTP(w, r)
		return w
	}
	w := call(http.MethodPost, "/api/v1/tokens", `{"name":"prom"}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "hearth_ro_") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{"name":"../x"}`, `{"name":"a","x":1}`, `{"name":"a"}{}`, `[]`, ``} {
		if c := call(http.MethodPost, "/api/v1/tokens", bad).Code; c != http.StatusBadRequest {
			t.Errorf("body %q: %d, want 400", bad, c)
		}
	}
	if c := call(http.MethodPost, "/api/v1/tokens", `{"name":"prom"}`).Code; c != http.StatusConflict {
		t.Errorf("duplicate: %d", c)
	}
	w = call(http.MethodGet, "/api/v1/tokens", "")
	if !strings.Contains(w.Body.String(), `"prom"`) || strings.Contains(w.Body.String(), "hearth_ro_") {
		t.Errorf("list: %s", w.Body)
	}
	if MetricsEnabled(context.Background(), st) {
		t.Error("metrics on by default")
	}
	if c := call(http.MethodPut, "/api/v1/tokens/metrics", `{"enabled":true}`).Code; c != http.StatusNoContent {
		t.Errorf("metrics on: %d", c)
	}
	if !MetricsEnabled(context.Background(), st) {
		t.Error("metrics did not turn on")
	}
	if c := call(http.MethodPut, "/api/v1/tokens/metrics", `{}`).Code; c != http.StatusBadRequest {
		t.Errorf("metrics without a value: %d", c)
	}
	if c := call(http.MethodDelete, "/api/v1/tokens/prom", "").Code; c != http.StatusNoContent {
		t.Errorf("revoke: %d", c)
	}
	if c := call(http.MethodDelete, "/api/v1/tokens/prom", "").Code; c != http.StatusNotFound {
		t.Errorf("revoke again: %d", c)
	}
	for _, want := range []string{"tokens.create prom ok", "tokens.metrics metrics ok: on", "tokens.revoke prom ok"} {
		if !strings.Contains(strings.Join(audited, "\n"), want) {
			t.Errorf("not audited: %q in %v", want, audited)
		}
	}
	// What the audit log cannot hold is not done.
	fail = true
	if c := call(http.MethodPost, "/api/v1/tokens", `{"name":"late"}`).Code; c != http.StatusInternalServerError {
		t.Errorf("unaudited create: %d", c)
	}
	for _, i := range tk.List() {
		if i.Name == "late" {
			t.Error("a token was made without an audit entry")
		}
	}
}
