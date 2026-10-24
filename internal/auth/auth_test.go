package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "hearth", "token"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func readToken(t *testing.T, m *Manager) string {
	t.Helper()
	b, err := os.ReadFile(m.TokenPath())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestTokenFileModesAndFormat(t *testing.T) {
	m := newTestManager(t)
	tok := readToken(t, m)
	if len(tok) != 64 || strings.Trim(tok, "0123456789abcdef") != "" {
		t.Fatalf("token %q is not 32 hex-encoded bytes", tok)
	}
	for p, want := range map[string]os.FileMode{m.TokenPath(): 0o600, filepath.Dir(m.TokenPath()): 0o700} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %o, want %o", p, fi.Mode().Perm(), want)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(m.TokenPath()))
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestRotateOnStart(t *testing.T) {
	m := newTestManager(t)
	first := readToken(t, m)
	m2, err := NewManager(m.TokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if readToken(t, m2) == first {
		t.Fatal("token not rotated on start")
	}
	if _, err := m2.Exchange(first); err == nil {
		t.Fatal("previous start's token accepted")
	}
}

func TestExchangeSingleUse(t *testing.T) {
	m := newTestManager(t)
	tok := readToken(t, m)

	key, err := m.Exchange(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Valid(key) {
		t.Fatal("new session key not valid")
	}
	if _, err := m.Exchange(tok); err == nil {
		t.Fatal("launch token reused")
	}
	next := readToken(t, m)
	if next == tok {
		t.Fatal("token file not rotated after exchange")
	}
	if _, err := m.Exchange(next); err != nil {
		t.Fatalf("fresh token from file rejected: %v", err)
	}
}

func TestExchangeRejects(t *testing.T) {
	m := newTestManager(t)
	tok := readToken(t, m)
	for _, bad := range []string{"", tok[:63], tok + "0", strings.ToUpper(tok), " " + tok} {
		if _, err := m.Exchange(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := m.Exchange(tok); err != nil {
		t.Fatalf("failed attempts burned the real token: %v", err)
	}
}

func TestConcurrentExchangeOnlyOneWins(t *testing.T) {
	m := newTestManager(t)
	tok := readToken(t, m)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Exchange(tok); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d exchanges succeeded with one token", wins)
	}
}

func TestSessionEviction(t *testing.T) {
	m := newTestManager(t)
	var keys []string
	for range MaxSessions + 1 {
		k, err := m.Exchange(readToken(t, m))
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if m.Valid(keys[0]) {
		t.Error("oldest session not evicted")
	}
	if !m.Valid(keys[len(keys)-1]) {
		t.Error("newest session evicted")
	}
}

func TestHandleAuth(t *testing.T) {
	m := newTestManager(t)
	tok := readToken(t, m)

	rec := httptest.NewRecorder()
	m.HandleAuth(rec, httptest.NewRequest(http.MethodGet, "/auth?t="+tok, nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	key, ok := strings.CutPrefix(loc, "/#k=")
	if !ok || !m.Valid(key) {
		t.Fatalf("Location %q", loc)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing Cache-Control: no-store")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("auth set a cookie; cookies leak to other localhost ports")
	}

	rec = httptest.NewRecorder()
	m.HandleAuth(rec, httptest.NewRequest(http.MethodGet, "/auth?t="+tok, nil))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Fatalf("reused token: status %d", rec.Code)
	}
}

func TestRequire(t *testing.T) {
	m := newTestManager(t)
	key, err := m.Exchange(readToken(t, m))
	if err != nil {
		t.Fatal(err)
	}
	h := m.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	cases := []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"no credentials", nil, 401},
		{"bearer ok", map[string]string{"Authorization": "Bearer " + key}, 204},
		{"bearer wrong", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"bearer empty", map[string]string{"Authorization": "Bearer "}, 401},
		{"basic scheme", map[string]string{"Authorization": "Basic " + key}, 401},
		{"ws subprotocol ok", map[string]string{"Upgrade": "websocket", "Sec-WebSocket-Protocol": Subprotocol + ", hearth.key." + key}, 204},
		{"ws subprotocol wrong", map[string]string{"Upgrade": "websocket", "Sec-WebSocket-Protocol": "hearth.key.nope"}, 401},
		{"subprotocol without upgrade", map[string]string{"Sec-WebSocket-Protocol": "hearth.key." + key}, 401},
		{"cookie ignored", map[string]string{"Cookie": "hearth_session=" + key}, 401},
		{"query ignored", nil, 401},
	}
	for _, c := range cases {
		target := "/api/v1/x"
		if c.name == "query ignored" {
			target += "?k=" + key
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: missing WWW-Authenticate", c.name)
		}
	}
}

func TestNewManagerRejectsRelativePath(t *testing.T) {
	if _, err := NewManager("token"); err == nil {
		t.Fatal("accepted relative token path")
	}
}
