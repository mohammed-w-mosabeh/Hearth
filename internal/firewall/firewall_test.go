package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fakeBroker struct {
	state broker.UfwState
	err   error // of a change
	calls []string
	last  any
}

func (b *fakeBroker) Call(_ context.Context, method string, params, out any) error {
	b.calls = append(b.calls, method)
	if method == "ufw.state" {
		*(out.(*broker.UfwState)) = b.state
		return nil
	}
	b.last = params
	return b.err
}

type fakeAudit struct{ got []store.AuditEntry }

func (a *fakeAudit) Audit(_ context.Context, e store.AuditEntry) error {
	a.got = append(a.got, e)
	return nil
}

type muxAdapter struct{ *http.ServeMux }

func (m muxAdapter) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

type env struct {
	b   *fakeBroker
	a   *fakeAudit
	mux *http.ServeMux
}

func newEnv() *env {
	e := &env{b: &fakeBroker{}, a: &fakeAudit{}, mux: http.NewServeMux()}
	e.b.state = broker.UfwState{Installed: true, Active: true, Incoming: "deny", Rules: []broker.UfwRule{
		{Action: "allow", Proto: "tcp", Port: 80, Family: "both", Editable: true},
		{Action: "limit", Proto: "tcp", Family: "both", Text: "limit tcp 2222"},
	}}
	reg := actions.NewRegistry(e.a)
	Register(reg, muxAdapter{e.mux}, e.b)
	e.mux.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	return e
}

type resp struct {
	Items   []actions.Item    `json:"items"`
	Blocked []actions.Blocked `json:"blocked"`
	Failed  []actions.Blocked `json:"failed"`
	Done    int               `json:"done"`
	Token   string            `json:"token"`
	Confirm string            `json:"confirm"`
}

func (e *env) post(t *testing.T, action string, body map[string]any) (int, resp) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/actions/firewall/"+action, bytes.NewReader(b)))
	var r resp
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	return rec.Code, r
}

func rule(action, port, source string) map[string]string {
	return map[string]string{"action": action, "proto": "tcp", "port": port, "source": source}
}

func TestRuleNeedsItsPreview(t *testing.T) {
	e := newEnv()
	if code, _ := e.post(t, "rule-add", map[string]any{"target": "ufw", "params": rule("allow", "443", "")}); code != http.StatusConflict {
		t.Fatalf("ran without a token: %d", code)
	}
	code, p := e.post(t, "rule-add", map[string]any{"target": "ufw", "params": rule("allow", "443", "10.0.0.0/8"), "preview": true})
	if code != http.StatusOK || p.Token == "" || len(p.Items) != 1 || p.Items[0].Name != "Allow 443/tcp from 10.0.0.0/8" {
		t.Fatalf("%d %+v", code, p)
	}
	if code, _ := e.post(t, "rule-add", map[string]any{"target": "ufw", "params": rule("allow", "444", "10.0.0.0/8"), "token": p.Token}); code != http.StatusConflict {
		t.Fatalf("a token for another rule: %d", code)
	}
	code, p = e.post(t, "rule-add", map[string]any{"target": "ufw", "params": rule("allow", "443", "10.0.0.0/8"), "preview": true})
	if code != http.StatusOK {
		t.Fatal(code)
	}
	code, r := e.post(t, "rule-add", map[string]any{"target": "ufw", "params": rule("allow", "443", "10.0.0.0/8"), "token": p.Token})
	if code != http.StatusOK || r.Done != 1 {
		t.Fatalf("%d %+v", code, r)
	}
	if got := e.b.last.(broker.UfwRuleParams); got.Op != "add" || got.Port != 443 || got.Source != "10.0.0.0/8" {
		t.Fatalf("%+v", got)
	}
	if len(e.a.got) == 0 || e.a.got[len(e.a.got)-1].Action != "firewall.rule-add" {
		t.Fatalf("%+v", e.a.got)
	}
}

func TestBadParamsNeverReachTheBroker(t *testing.T) {
	e := newEnv()
	for _, params := range []map[string]string{
		{},
		{"action": "reject", "proto": "tcp", "port": "22"},
		{"action": "allow", "proto": "icmp", "port": "22"},
		{"action": "allow", "proto": "tcp", "port": "22 80"},
		{"action": "allow", "proto": "tcp", "port": "22", "source": "1.2.3.4 port 22"},
		{"action": "allow", "proto": "tcp", "port": "22", "interface": "eth0"},
	} {
		if code, _ := e.post(t, "rule-add", map[string]any{"target": "ufw", "params": params, "preview": true}); code != http.StatusBadRequest {
			t.Errorf("%v: %d", params, code)
		}
	}
	if code, _ := e.post(t, "rule-add", map[string]any{"target": "nftables", "params": rule("allow", "22", ""), "preview": true}); code != http.StatusBadRequest {
		t.Errorf("target: %d", code)
	}
	if len(e.b.calls) != 0 {
		t.Fatalf("%v", e.b.calls)
	}
}

func TestPlanBlocks(t *testing.T) {
	e := newEnv()
	preview := func(action string, p map[string]string) resp {
		t.Helper()
		_, r := e.post(t, action, map[string]any{"target": "ufw", "params": p, "preview": true})
		return r
	}
	blocked := func(r resp, reason string) {
		t.Helper()
		if len(r.Items) != 0 || len(r.Blocked) != 1 || r.Blocked[0].Reason != reason || r.Token != "" {
			t.Fatalf("want %s: %+v", reason, r)
		}
	}
	blocked(preview("rule-add", rule("allow", "80", "")), ReasonExists)
	blocked(preview("rule-delete", rule("allow", "81", "")), ReasonMissing)
	e.b.state.Active = false
	blocked(preview("rule-add", rule("allow", "81", "")), ReasonInactive)
	e.b.state.Installed = false
	blocked(preview("enable", nil), ReasonNoTool)
	e.b.state.Installed, e.b.state.Active = true, true
	blocked(preview("enable", nil), ReasonSame)
}

func TestSSHWarnings(t *testing.T) {
	e := newEnv()
	preview := func(action string, p map[string]string) string {
		t.Helper()
		_, r := e.post(t, action, map[string]any{"target": "ufw", "params": p, "preview": true})
		if len(r.Items) != 1 {
			t.Fatalf("%+v", r)
		}
		return r.Items[0].Detail
	}
	if d := preview("rule-add", rule("deny", "22", "")); !strings.Contains(d, "SSH") {
		t.Fatalf("%q", d)
	}
	if d := preview("rule-add", rule("allow", "22", "")); d != "" {
		t.Fatalf("%q", d)
	}
	e.b.state.Rules = append(e.b.state.Rules, broker.UfwRule{Action: "allow", Proto: "tcp", Port: 22, Editable: true})
	if d := preview("rule-delete", rule("allow", "22", "")); !strings.Contains(d, "SSH") {
		t.Fatalf("%q", d)
	}
	e.b.state.Active = false
	if d := preview("enable", nil); d != "" {
		t.Fatalf("a rule allows SSH: %q", d)
	}
	e.b.state.Rules = nil
	if d := preview("enable", nil); !strings.Contains(d, "SSH") {
		t.Fatalf("%q", d)
	}
}

func TestDisableNeedsTheWords(t *testing.T) {
	e := newEnv()
	_, p := e.post(t, "disable", map[string]any{"target": "ufw", "preview": true})
	if p.Confirm != ConfirmDisable || p.Token == "" {
		t.Fatalf("%+v", p)
	}
	if code, _ := e.post(t, "disable", map[string]any{"target": "ufw", "token": p.Token, "confirm": "yes"}); code != http.StatusBadRequest {
		t.Fatalf("%d", code)
	}
	if e.b.last != nil {
		t.Fatal("it ran")
	}
	_, p = e.post(t, "disable", map[string]any{"target": "ufw", "preview": true})
	code, r := e.post(t, "disable", map[string]any{"target": "ufw", "token": p.Token, "confirm": ConfirmDisable})
	if code != http.StatusOK || r.Done != 1 || e.b.last.(broker.UfwSetParams).Enabled {
		t.Fatalf("%d %+v %+v", code, r, e.b.last)
	}
}

func TestFailuresAreReported(t *testing.T) {
	e := newEnv()
	for want, err := range map[string]error{
		ReasonUnauthorized: broker.ErrUnauthorized,
		ReasonError:        broker.Errorf(broker.CodeFailed, "ufw: it failed"),
	} {
		e.b.err = err
		_, p := e.post(t, "rule-delete", map[string]any{"target": "ufw", "params": rule("allow", "80", ""), "preview": true})
		code, r := e.post(t, "rule-delete", map[string]any{"target": "ufw", "params": rule("allow", "80", ""), "token": p.Token})
		if code != http.StatusOK || r.Done != 0 || len(r.Failed) != 1 || r.Failed[0].Reason != want {
			t.Fatalf("%v: %d %+v", err, code, r)
		}
	}
	e.b.err = errors.New("dial unix /secret")
	_, p := e.post(t, "rule-delete", map[string]any{"target": "ufw", "params": rule("allow", "80", ""), "preview": true})
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"target": "ufw", "params": rule("allow", "80", ""), "token": p.Token})
	e.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/actions/firewall/rule-delete", bytes.NewReader(b)))
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestStateRoute(t *testing.T) {
	e := newEnv()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/firewall", nil))
	var s broker.UfwState
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || rec.Code != http.StatusOK || !s.Active || len(s.Rules) != 2 {
		t.Fatalf("%d %+v %v", rec.Code, s, err)
	}
}

func FuzzParseRule(f *testing.F) {
	f.Add("allow", "tcp", "22", "10.0.0.0/8")
	f.Add("deny", "udp", "x", "")
	f.Fuzz(func(t *testing.T, action, proto, port, source string) {
		p, err := parseRule("add", actions.Request{Target: Target, Params: map[string]string{"action": action, "proto": proto, "port": port, "source": source}})
		if err != nil {
			return
		}
		if _, err := broker.CheckUfwRule(p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	})
}
