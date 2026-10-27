package homepage

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestValidURL(t *testing.T) {
	for s, ok := range map[string]bool{
		"http://192.168.1.10:8080/": true, "https://nas.local/admin?x=1": true, "http://[::1]:3000": true,
		"": false, "ftp://x": false, "javascript:alert(1)": false, "http://": false, "http://u:p@host/": false,
		"http://host/ a": false, "http://host:99999/": false, "//host/": false, "http://host/\n": false,
		"http://" + strings.Repeat("a", 2050): false,
	} {
		if err := ValidURL(s); (err == nil) != ok {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCheck(t *testing.T) {
	var heads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials sent")
		}
		if r.Method == http.MethodHead {
			heads.Add(1)
		}
	})
	mux.HandleFunc("/nohead", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	mux.HandleFunc("/once", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/twice", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/once", http.StatusFound) })
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tlsSrv := httptest.NewTLSServer(mux)
	defer tlsSrv.Close()

	c := NewChecker(nil, nil, quiet())
	for path, want := range map[string]string{
		"/ok": "up", "/nohead": "up", "/login": "up", "/broken": "down", "/once": "up", "/twice": "down", "/away": "down",
	} {
		if st := c.Check(context.Background(), srv.URL+path); st.State != want {
			t.Errorf("%s: %+v, want %s", path, st, want)
		}
	}
	if heads.Load() == 0 {
		t.Error("HEAD not tried first")
	}
	if st := c.Check(context.Background(), tlsSrv.URL+"/ok"); st.State != "warn" {
		t.Errorf("self-signed: %+v", st)
	}
	if st := c.Check(context.Background(), "http://127.0.0.1:1/"); st.State != "down" || st.Error != "connection refused" {
		t.Errorf("refused: %+v", st)
	}
	start := time.Now()
	if st := c.Check(context.Background(), srv.URL+"/slow"); st.State != "down" || time.Since(start) > 7*time.Second {
		t.Errorf("slow: %+v after %v", st, time.Since(start))
	}
}

type hub struct {
	mu    sync.Mutex
	subs  int
	msgs  []string
	watch chan struct{}
}

func (h *hub) Publish(_ string, v any) error {
	b, _ := json.Marshal(v)
	h.mu.Lock()
	h.msgs = append(h.msgs, string(b))
	h.mu.Unlock()
	return nil
}
func (h *hub) Subscribers(string) int       { h.mu.Lock(); defer h.mu.Unlock(); return h.subs }
func (h *hub) Watch(string) <-chan struct{} { return h.watch }
func (h *hub) set(n int)                    { h.mu.Lock(); h.subs = n; h.mu.Unlock(); h.watch <- struct{}{} }
func (h *hub) count() int                   { h.mu.Lock(); defer h.mu.Unlock(); return len(h.msgs) }
func (h *hub) last() string                 { h.mu.Lock(); defer h.mu.Unlock(); return h.msgs[len(h.msgs)-1] }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAPIAndChecker(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	db := openDB(t)
	h := &hub{watch: make(chan struct{}, 1)}
	c := NewChecker(db, h, quiet())
	a := &API{DB: db, Checker: c}
	mux := http.NewServeMux()
	a.Register(mux)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	for _, bad := range []string{
		`{"name":"","url":"http://x/"}`, `{"name":"x","url":"javascript:alert(1)"}`, `{"name":"x","url":"http://x/","icon":"upload:` + strings.Repeat("a", 64) + `"}`,
		`{"name":"x","url":"http://x/","icon":"http://evil/x.png"}`, `{"name":"x","url":"http://x/","container":"../x"}`,
		`{"name":"x","url":"http://x/","admin":true}`, `{"name":"a\u0000b","url":"http://x/"}`,
	} {
		if rec := do("POST", "/api/v1/homepage/tiles", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// An uploaded SVG icon, sanitized.
	rec := do("POST", "/api/v1/homepage/icons", `<svg xmlns="http://www.w3.org/2000/svg" onload="x"><circle r="1"/></svg>`)
	var up struct{ Icon, Data string }
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(up.Data, "data:image/svg+xml;base64,") {
		t.Fatalf("upload %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/v1/homepage/icons", `<html>no</html>`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad upload %d", rec.Code)
	}

	rec = do("POST", "/api/v1/homepage/tiles", `{"name":" NAS ","url":"`+target.URL+`/","icon":"`+up.Icon+`","group":"Home","health_check":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	_ = do("POST", "/api/v1/homepage/tiles", `{"name":"Docs","url":"https://example.invalid/","icon":"builtin:book"}`)

	rec = do("GET", "/api/v1/homepage", "")
	var list struct {
		Tiles []TileView
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Tiles) != 2 || list.Tiles[0].Name != "NAS" || list.Tiles[0].IconData == "" || list.Tiles[1].IconData != "" {
		t.Fatalf("list %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "onload") {
		t.Fatal("unsanitized icon stored")
	}

	// The checker runs only while subscribed, and only enabled tiles.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("checked without subscribers")
	}
	h.set(1)
	waitFor(t, "status", func() bool { return h.count() > 0 && strings.Contains(h.last(), `"state":"up"`) })
	if strings.Count(h.last(), `"state"`) != 1 {
		t.Fatalf("checked a tile without health_check: %s", h.last())
	}
	// Editing a tile checks again.
	n := hits.Load()
	id := list.Tiles[0].ID
	if rec := do("PUT", "/api/v1/homepage/tiles/"+itoa(id), `{"name":"NAS","url":"`+target.URL+`/x","health_check":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("update %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "recheck", func() bool { return hits.Load() > n })
	h.set(0)
	time.Sleep(30 * time.Millisecond)

	if rec := do("PUT", "/api/v1/homepage/order", `{"ids":[`+itoa(list.Tiles[1].ID)+`]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("order %d", rec.Code)
	}
	rec = do("GET", "/api/v1/homepage", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Tiles[0].Name != "Docs" {
		t.Fatalf("order not applied %s", rec.Body.String())
	}
	if rec := do("DELETE", "/api/v1/homepage/tiles/"+itoa(id), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/homepage/tiles/"+itoa(id), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice %d", rec.Code)
	}
	cancel()
	<-done
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
