package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *memSettings) Setting(_ context.Context, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *memSettings) SetSetting(_ context.Context, key string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = v
	return nil
}

func TestSettings(t *testing.T) {
	h := newHarness(t)
	st := &memSettings{m: map[string]json.RawMessage{}}
	h.s.RegisterSettings(st)
	get := func(key string) result {
		return h.do(t, req{path: "/api/v1/settings/" + key, key: h.key})
	}
	put := func(key, body string) result {
		return h.do(t, req{method: http.MethodPut, path: "/api/v1/settings/" + key, key: h.key, origin: h.origin(), body: body})
	}

	if r := get("home.layout"); r.code != 200 || strings.TrimSpace(r.body) != "null" {
		t.Fatalf("unset: %d %q", r.code, r.body)
	}
	if r := put("home.layout", `{"order": ["cpu", "disk:/home", "gpu:0000:03:00.0"], "hidden": ["temps"]}`); r.code != 204 {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	if r := get("home.layout"); r.body != `{"order":["cpu","disk:/home","gpu:0000:03:00.0"],"hidden":["temps"]}` {
		t.Fatalf("roundtrip %q", r.body)
	}

	for _, bad := range []string{
		``, `null`, `[]`, `{"order": "cpu"}`, `{"order": [], "x": 1}`,
		`{"order": ["<script>"]}`, `{"order": ["CPU"]}`, `{"order": ["` + strings.Repeat("a", 300) + `"]}`,
		`{"hidden": [` + strings.Repeat(`"cpu",`, 300) + `"cpu"]}`,
	} {
		if r := put("home.layout", bad); r.code != 400 && r.code != 413 {
			t.Errorf("%.40q: %d", bad, r.code)
		}
	}
	if r := put("home.layout", `{"order": ["`+strings.Repeat("a", 20<<10)+`"]}`); r.code != 413 {
		t.Errorf("oversized: %d", r.code)
	}
	if r := get("token"); r.code != 404 {
		t.Errorf("unknown key get: %d", r.code)
	}
	if r := put("token", `{}`); r.code != 404 {
		t.Errorf("unknown key put: %d", r.code)
	}
	if r := h.do(t, req{method: http.MethodPut, path: "/api/v1/settings/home.layout", key: h.key, body: `{"order":[]}`}); r.code != 403 {
		t.Errorf("no Origin: %d", r.code)
	}
	if r := put("apps.favorites", `{"ids": ["org.gnome.TextEditor", "Among Us", "appimage.obsidian-1.5.3"]}`); r.code != 204 {
		t.Fatalf("favorites: %d %s", r.code, r.body)
	}
	for _, bad := range []string{`{"ids": ["../x"]}`, `{"ids": ["a\u0000b"]}`, `{"ids": [""]}`, `{"ids": "x"}`, `[]`} {
		if r := put("apps.favorites", bad); r.code != 400 {
			t.Errorf("favorites %s: %d", bad, r.code)
		}
	}
	if len(st.m) != 2 {
		t.Fatalf("store %v", st.m)
	}
}
