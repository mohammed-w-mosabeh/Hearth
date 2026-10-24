package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type memAudit struct {
	mu      sync.Mutex
	entries []store.AuditEntry
}

func (m *memAudit) Audit(_ context.Context, e store.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

type harness struct {
	t     *testing.T
	reg   *Registry
	audit *memAudit
	mux   *http.ServeMux
	ran   []Plan
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, audit: &memAudit{}, mux: http.NewServeMux(), now: time.Unix(1_700_000_000, 0)}
	h.reg = NewRegistry(h.audit)
	h.reg.now = func() time.Time { return h.now }
	validate := func(r Request) error {
		if !strings.HasPrefix(r.Target, "thing:") {
			return errors.New("bad target")
		}
		return nil
	}
	plan := func(_ context.Context, r Request) (Plan, error) {
		if r.Target == "thing:gone" {
			return Plan{}, Errorf(http.StatusNotFound, "no such thing")
		}
		p := Plan{Summary: "remove " + r.Target, Items: []Item{{ID: r.Target, Name: "Thing"}}, State: r.Target + "@preview",
			Blocked: []Blocked{{Item: Item{ID: "thing:root", Name: "Root"}, Reason: "system"}}}
		if r.Params["big"] == "yes" {
			p.Confirm = "Thing"
		}
		return p, nil
	}
	run := func(_ context.Context, _ Request, p Plan) (Result, error) {
		h.ran = append(h.ran, p)
		return Result{Done: len(p.Items)}, nil
	}
	h.reg.Register(Action{Kind: "thing", Name: "remove", Risk: Destructive, Validate: validate, Plan: plan, Run: run})
	h.reg.Register(Action{Kind: "thing", Name: "pause", Risk: Reversible, Validate: validate, Plan: plan, Run: run})
	h.mux.HandleFunc("POST /api/v1/actions/{kind}/{action}", h.reg.Handle)
	return h
}

func (h *harness) post(path, body string) (int, map[string]any) {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestDestructiveNeedsPreviewToken(t *testing.T) {
	h := newHarness(t)
	const p = "/api/v1/actions/thing/remove"
	if c, _ := h.post(p, `{"target":"thing:a"}`); c != http.StatusConflict {
		t.Fatalf("no token: %d", c)
	}
	if c, _ := h.post(p, `{"target":"thing:a","token":"made-up"}`); c != http.StatusConflict {
		t.Fatalf("bad token: %d", c)
	}
	c, prev := h.post(p, `{"target":"thing:a","preview":true}`)
	tok, _ := prev["token"].(string)
	if c != 200 || tok == "" || prev["risk"] != "destructive" || prev["summary"] != "remove thing:a" {
		t.Fatalf("preview: %d %v", c, prev)
	}
	if len(h.ran) != 0 || len(h.audit.entries) != 0 {
		t.Fatal("preview executed or audited")
	}
	// The token is bound to the target.
	if c, _ := h.post(p, `{"target":"thing:b","token":"`+tok+`"}`); c != http.StatusConflict {
		t.Fatalf("other target: %d", c)
	}
	// ...and was consumed by that attempt.
	if c, _ := h.post(p, `{"target":"thing:a","token":"`+tok+`"}`); c != http.StatusConflict {
		t.Fatalf("reused token: %d", c)
	}

	_, prev = h.post(p, `{"target":"thing:a","preview":true}`)
	tok = prev["token"].(string)
	if c, res := h.post(p, `{"target":"thing:a","token":"`+tok+`"}`); c != 200 || res["done"] != 1.0 {
		t.Fatalf("run: %d %v", c, res)
	}
	// Run gets the previewed plan, not a fresh one.
	if len(h.ran) != 1 || h.ran[0].State != "thing:a@preview" {
		t.Fatalf("ran %+v", h.ran)
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != "thing.remove" || h.audit.entries[0].Result != "ok" {
		t.Fatalf("audit %+v", h.audit.entries)
	}
}

func TestTokenExpiresAndBindsParams(t *testing.T) {
	h := newHarness(t)
	const p = "/api/v1/actions/thing/remove"
	_, prev := h.post(p, `{"target":"thing:a","preview":true}`)
	h.now = h.now.Add(TokenTTL + time.Second)
	if c, _ := h.post(p, `{"target":"thing:a","token":"`+prev["token"].(string)+`"}`); c != http.StatusConflict {
		t.Fatalf("expired: %d", c)
	}
	_, prev = h.post(p, `{"target":"thing:a","preview":true}`)
	if c, _ := h.post(p, `{"target":"thing:a","params":{"x":"1"},"token":"`+prev["token"].(string)+`"}`); c != http.StatusConflict {
		t.Fatalf("changed params: %d", c)
	}
}

func TestHighImpactNeedsTypedName(t *testing.T) {
	h := newHarness(t)
	const p = "/api/v1/actions/thing/remove"
	_, prev := h.post(p, `{"target":"thing:a","params":{"big":"yes"},"preview":true}`)
	if prev["confirm"] != "Thing" {
		t.Fatalf("preview %v", prev)
	}
	if c, _ := h.post(p, `{"target":"thing:a","params":{"big":"yes"},"token":"`+prev["token"].(string)+`","confirm":"thing"}`); c != 400 {
		t.Fatalf("wrong confirm: %d", c)
	}
	_, prev = h.post(p, `{"target":"thing:a","params":{"big":"yes"},"preview":true}`)
	if c, _ := h.post(p, `{"target":"thing:a","params":{"big":"yes"},"token":"`+prev["token"].(string)+`","confirm":"Thing"}`); c != 200 {
		t.Fatalf("right confirm: %d", c)
	}
}

func TestReversibleRunsDirectly(t *testing.T) {
	h := newHarness(t)
	c, res := h.post("/api/v1/actions/thing/pause", `{"target":"thing:a"}`)
	if c != 200 || len(h.ran) != 1 || len(h.audit.entries) != 1 {
		t.Fatalf("pause: %d ran=%d", c, len(h.ran))
	}
	if b, _ := res["blocked"].([]any); len(b) != 1 {
		t.Fatalf("blocked items not reported: %v", res)
	}
	if _, prev := h.post("/api/v1/actions/thing/pause", `{"target":"thing:a","preview":true}`); prev["token"] != nil {
		t.Fatalf("token issued for a reversible action: %v", prev)
	}
}

func TestRejections(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{"/api/v1/actions/thing/explode", `{"target":"thing:a"}`, 404},
		{"/api/v1/actions/thing/pause", `{"target":"other"}`, 400},
		{"/api/v1/actions/thing/pause", `{"target":""}`, 400},
		{"/api/v1/actions/thing/pause", `{"target":"thing:a","extra":1}`, 400},
		{"/api/v1/actions/thing/pause", `not json`, 400},
		{"/api/v1/actions/thing/pause", `{"target":"thing:gone"}`, 404},
	} {
		if got, _ := h.post(c.path, c.body); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.path, c.body, got, c.want)
		}
	}
	if len(h.ran) != 0 {
		t.Fatal("a rejected request ran")
	}
}
