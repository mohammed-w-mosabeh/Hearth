package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"
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
	if r := put("home.snoozed", `{"until": {"unit:system/a b.service": 1700000000000, "listener:tcp [::]:22": 1, "updates.security": 2}}`); r.code != 204 {
		t.Fatalf("snoozed: %d %s", r.code, r.body)
	}
	for _, bad := range []string{
		`{}`, `{"until": null}`, `{"until": []}`, `{"until": {"a": 0}}`, `{"until": {"a": -1}}`, `{"until": {"a": 1.5}}`,
		`{"until": {"a": "1"}}`, `{"until": {"A:x": 1}}`, `{"until": {"a:\n": 1}}`, `{"until": {"a:\u0085": 1}}`, `{"until": {"a:": 1}}`, `{"until": {"": 1}}`,
		`{"until": {"a": 1}, "x": 1}`, `{"until": {"a:` + strings.Repeat("x", 300) + `": 1}}`,
		`{"until": {` + manyIDs(201) + `}}`,
	} {
		if r := put("home.snoozed", bad); r.code != 400 {
			t.Errorf("snoozed %.50q: %d", bad, r.code)
		}
	}
	if len(st.m) != 3 {
		t.Fatalf("store %v", st.m)
	}
}

func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = `"alert:` + strconv.Itoa(i) + `": 1`
	}
	return strings.Join(ids, ", ")
}

func FuzzSettingValidators(f *testing.F) {
	for _, s := range []string{`{"until":{"unit:system/a.service":1}}`, `{"until":{"a:\u0000":1}}`, `{"order":["cpu","tile:12"],"hidden":[]}`, `null`, `{"ids":["firefox.desktop"]}`, `{"ids":["a/b"]}`, `{"order":["x\n"]}`, `{"x":1}`} {
		f.Add([]byte(s))
	}
	bad := func(ids []string, max int) bool {
		for _, id := range ids {
			if id == "" || len(id) > max || strings.ContainsFunc(id, unicode.IsControl) || strings.ContainsRune(id, 0) {
				return true
			}
		}
		return false
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if validateHomeLayout(b) == nil {
			var l struct{ Order, Hidden []string }
			if json.Unmarshal(b, &l) != nil || len(l.Order) > 256 || len(l.Hidden) > 256 || bad(append(l.Order, l.Hidden...), 200) {
				t.Fatalf("layout accepted %q", b)
			}
		}
		if validateSnoozed(b) == nil {
			var v struct{ Until map[string]int64 }
			if json.Unmarshal(b, &v) != nil || v.Until == nil || len(v.Until) > 200 {
				t.Fatalf("snoozed accepted %q", b)
			}
			for id, until := range v.Until {
				if bad([]string{id}, 300) || until <= 0 {
					t.Fatalf("snoozed accepted %q", b)
				}
			}
		}
		if validateFavorites(b) == nil {
			var v struct{ IDs []string }
			if json.Unmarshal(b, &v) != nil || len(v.IDs) > 500 || bad(v.IDs, 255) || strings.Contains(strings.Join(v.IDs, ""), "/") {
				t.Fatalf("favorites accepted %q", b)
			}
		}
	})
}
