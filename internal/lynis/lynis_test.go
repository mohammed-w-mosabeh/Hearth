package lynis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fakeBroker struct {
	state   broker.LynisState
	err     error
	calls   []string
	params  []any
	release chan struct{}
}

func (b *fakeBroker) Call(ctx context.Context, method string, params, out any) error {
	b.calls = append(b.calls, method)
	b.params = append(b.params, params)
	if method == "lynis.run" && b.release != nil {
		<-b.release
	}
	if b.err != nil {
		return b.err
	}
	if p, ok := out.(*broker.LynisState); ok {
		*p = b.state
	}
	return nil
}

type fakeStore struct{ runs []store.LynisRun } // newest first

func (s *fakeStore) AddLynisRun(_ context.Context, r store.LynisRun) error {
	s.runs = append([]store.LynisRun{r}, s.runs...)
	return nil
}

func (s *fakeStore) LynisRuns(_ context.Context, limit int) ([]store.LynisRun, error) {
	return s.runs[:min(limit, len(s.runs))], nil
}

type fakeAudit struct {
	got []store.AuditEntry
	err error
}

func (a *fakeAudit) Audit(_ context.Context, e store.AuditEntry) error {
	a.got = append(a.got, e)
	return a.err
}

type env struct {
	m        *Manager
	b        *fakeBroker
	s        *fakeStore
	a        *fakeAudit
	observed []map[string]float64
	mux      *http.ServeMux
}

type muxAdapter struct{ *http.ServeMux }

func (m muxAdapter) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lynis")
	if err := os.WriteFile(bin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e := &env{b: &fakeBroker{}, s: &fakeStore{}, a: &fakeAudit{}, mux: http.NewServeMux()}
	e.b.state = broker.LynisState{Installed: true, Schedule: "off"}
	e.m = New(Config{Broker: e.b, Store: e.s, Audit: e.a, Binary: bin,
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) { e.observed = append(e.observed, v) }})
	e.m.Register(muxAdapter{e.mux})
	return e
}

func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func report(at int64, index int) *broker.LynisReport {
	return &broker.LynisReport{Time: at, Index: index, Warnings: []broker.LynisFinding{{Text: "x"}}, Suggestions: []broker.LynisFinding{}}
}

func TestIngest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	step := func(r *broker.LynisReport, want string) {
		t.Helper()
		if err := e.m.ingest(ctx, r); err != nil {
			t.Fatal(err)
		}
		last := e.observed[len(e.observed)-1]
		if want == "" && len(last) != 0 {
			t.Fatalf("unexpected alert: %v", last)
		}
		if want != "" && last[want] != 1 {
			t.Fatalf("want %q: %v", want, last)
		}
	}
	step(nil, "")
	step(report(100, 70), "") // the first run is a baseline
	step(report(100, 70), "") // seen
	step(report(200, 72), "") // rose
	step(report(300, 65), "from 72 to 65")
	step(report(300, 65), "") // once: this ends the incident
	step(report(400, 65), "") // level
	step(report(350, 10), "") // older than what is stored
	if len(e.s.runs) != 4 || e.s.runs[0].Index != 65 || e.s.runs[0].Warnings != 1 {
		t.Fatalf("%+v", e.s.runs)
	}
}

func TestReadingStartsNothing(t *testing.T) {
	e := newEnv(t)
	e.m.watch(context.Background())
	if rec := e.do("GET", "/api/v1/security/lynis", ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	for _, c := range e.b.calls {
		if c != "lynis.report" {
			t.Fatalf("called %s", c)
		}
	}
}

func TestNotInstalledAsksNoBroker(t *testing.T) {
	e := newEnv(t)
	e.m.cfg.Binary = "/nonexistent"
	e.m.watch(context.Background())
	rec := e.do("GET", "/api/v1/security/lynis", "")
	var v View
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Installed || v.Schedule != "off" || len(e.b.calls) != 0 {
		t.Fatalf("%+v %v", v, e.b.calls)
	}
}

func TestRunIsAuditedAndStored(t *testing.T) {
	e := newEnv(t)
	e.b.state.Report = report(100, 70)
	rec := e.do("POST", "/api/v1/security/lynis/run", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(e.s.runs) != 1 {
		t.Fatalf("%+v", e.s.runs)
	}
	if len(e.a.got) != 1 || e.a.got[0].Action != "security.lynis.run" || e.a.got[0].Result != "ok: hardening index 70" {
		t.Fatalf("%+v", e.a.got)
	}
	var v View
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Running || len(v.History) != 1 || v.Report == nil {
		t.Fatalf("%+v", v)
	}
}

func TestRunFailures(t *testing.T) {
	e := newEnv(t)
	e.b.err = broker.ErrUnauthorized
	if rec := e.do("POST", "/api/v1/security/lynis/run", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
	if len(e.a.got) != 1 || !strings.HasPrefix(e.a.got[0].Result, "error: ") {
		t.Fatalf("a refusal is audited too: %+v", e.a.got)
	}
	e.b.err = errors.New("dial unix /secret/path")
	rec := e.do("POST", "/api/v1/security/lynis/run", "")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e.b.err, e.a.err = nil, errors.New("disk full")
	if rec := e.do("POST", "/api/v1/security/lynis/run", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("no audit, no report: %d", rec.Code)
	}
}

func TestOneRunAtATime(t *testing.T) {
	e := newEnv(t)
	e.b.release = make(chan struct{})
	done := make(chan int)
	go func() { done <- e.do("POST", "/api/v1/security/lynis/run", "").Code }()
	for !e.m.running.Load() {
		time.Sleep(time.Millisecond)
	}
	if rec := e.do("POST", "/api/v1/security/lynis/run", ""); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
	close(e.b.release)
	if code := <-done; code != http.StatusOK {
		t.Fatal(code)
	}
}

func TestSchedule(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("PUT", "/api/v1/security/lynis/schedule", `{"every":"weekly"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if e.b.calls[0] != "lynis.schedule" || e.b.params[0].(broker.LynisScheduleParams).Every != "weekly" {
		t.Fatalf("%v %v", e.b.calls, e.b.params)
	}
	if len(e.a.got) != 1 || e.a.got[0].Action != "security.lynis.schedule" || e.a.got[0].Target != "weekly" {
		t.Fatalf("%+v", e.a.got)
	}
	for _, bad := range []string{`nope`, `{"every":"weekly","x":1}`} {
		if rec := e.do("PUT", "/api/v1/security/lynis/schedule", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	e.b.err = broker.Errorf(broker.CodeBadParams, "every must be off, weekly or monthly")
	n := len(e.a.got)
	if rec := e.do("PUT", "/api/v1/security/lynis/schedule", `{"every":"daily"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	if len(e.a.got) != n {
		t.Fatal("a value the broker refused reached the audit log's target")
	}
}
