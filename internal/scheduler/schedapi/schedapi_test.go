package schedapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/scheduler"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// memWriter keeps jobs as unit files in dir (so Store can read them) and
// records what it was asked.
type memWriter struct {
	dir     string
	calls   []string
	scripts map[string]string
	fail    error
}

func (m *memWriter) Save(_ context.Context, j scheduler.Job, paused bool) error {
	if m.fail != nil {
		return m.fail
	}
	m.calls = append(m.calls, "save "+j.ID)
	m.scripts[j.ID] = j.Script
	base := filepath.Join(m.dir, scheduler.UnitBase(j.ID))
	_ = os.WriteFile(base+".service", []byte(scheduler.ServiceFile(j, "/run")), 0o600)
	return os.WriteFile(base+".timer", []byte(scheduler.TimerFile(j)), 0o600)
}

func (m *memWriter) Delete(_ context.Context, id string) error {
	if m.fail != nil {
		return m.fail
	}
	m.calls = append(m.calls, "delete "+id)
	base := filepath.Join(m.dir, scheduler.UnitBase(id))
	_ = os.Remove(base + ".service")
	return os.Remove(base + ".timer")
}

func (m *memWriter) Run(_ context.Context, id string) error {
	if m.fail != nil {
		return m.fail
	}
	m.calls = append(m.calls, "run "+id)
	return nil
}

func (m *memWriter) SetPaused(_ context.Context, id string, p bool) error {
	m.calls = append(m.calls, "pause "+id)
	return m.fail
}

func (m *memWriter) Script(_ context.Context, id string) (string, error) {
	return m.scripts[id], m.fail
}

type noBus struct{}

func (noBus) Call(context.Context, string, dbus.ObjectPath, string, []any, ...any) error {
	return errors.New("no systemd here")
}
func (noBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, errors.New("no systemd here")
}
func (noBus) Close() error { return nil }

type auditFunc func(store.AuditEntry)

func (f auditFunc) Audit(_ context.Context, e store.AuditEntry) error { f(e); return nil }

func TestAPI(t *testing.T) {
	dir := t.TempDir()
	w := &memWriter{dir: dir, scripts: map[string]string{}}
	s := &scheduler.Store{Scope: scheduler.ScopeUser, UnitDir: dir, W: w,
		Bus: func(context.Context) (scheduler.Bus, error) { return noBus{}, nil }}
	var audits []store.AuditEntry
	run := func(_ context.Context, name string, _ ...string) (safeexec.Result, error) {
		if name == "timespan-check" || name == "calendar-check" {
			return safeexec.Result{Stdout: []byte("Normalized form: *-*-* 00:00:00\n")}, nil
		}
		return safeexec.Result{}, errors.New("unexpected " + name)
	}
	a := &API{Stores: map[string]*scheduler.Store{scheduler.ScopeUser: s}, Run: run, Log: slog.Default(),
		Audit: func(_ context.Context, e store.AuditEntry) error { audits = append(audits, e); return nil }}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/jobs", a.HandleList)
	mux.HandleFunc("GET /api/v1/jobs/{scope}/{id}", a.HandleGet)
	mux.HandleFunc("POST /api/v1/jobs/{scope}", a.HandleSave)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	sample := scheduler.Job{Name: "Backup", Script: "#!/bin/sh\necho hi\n", Timeout: 600, Notify: true,
		Schedule: scheduler.Schedule{Kind: scheduler.KindCalendar, Value: "daily"}}
	body, _ := json.Marshal(sample)
	rec := do("POST", "/api/v1/jobs/user", string(body))
	var saved scheduler.Job
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &saved) != nil || !scheduler.ValidID(saved.ID) {
		t.Fatalf("save %d %s", rec.Code, rec.Body)
	}
	if len(audits) != 1 || audits[0].Action != "job/save" || audits[0].Target != "user/"+saved.ID || audits[0].Result != "ok" {
		t.Errorf("audit %+v", audits)
	}
	if rec := do("GET", "/api/v1/jobs", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Backup"`) {
		t.Errorf("list %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/api/v1/jobs/user/"+saved.ID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"script":"#!/bin/sh\necho hi\n"`) {
		t.Errorf("get %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/v1/jobs/system", string(body)}, // no root store here
		{"/api/v1/jobs/user", `{"name":"x"}`},
		{"/api/v1/jobs/user", `{"name":"x","script":"#!/bin/sh","timeout":5,"schedule":{"kind":"calendar","value":"daily"},"extra":1}`},
		{"/api/v1/jobs/user", `{"name":"x","script":"#!/bin/sh","timeout":5,"schedule":{"kind":"calendar","value":"daily\nExecStart=/x"}}`},
	} {
		if rec := do("POST", c.path, c.body); rec.Code != 400 {
			t.Errorf("%s %s: %d", c.path, c.body, rec.Code)
		}
	}
	if rec := do("GET", "/api/v1/jobs/user/deadbeef", ""); rec.Code != 404 {
		t.Errorf("unknown job: %d", rec.Code)
	}

	// Actions: delete is previewed and confirmed; a polkit refusal is "unauthorized".
	reg := actions.NewRegistry(auditFunc(func(e store.AuditEntry) { audits = append(audits, e) }))
	a.Register(reg)
	act := func(action, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/actions/job/"+action, strings.NewReader(body))
		r.SetPathValue("kind", "job")
		r.SetPathValue("action", action)
		reg.Handle(rec, r)
		return rec
	}
	target := `{"target":"user/` + saved.ID + `"`
	if rec := act("run", target+`}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done":1`) {
		t.Errorf("run %d %s", rec.Code, rec.Body)
	}
	w.fail = scheduler.ErrUnauthorized
	if rec := act("run", target+`}`); !strings.Contains(rec.Body.String(), `"reason":"unauthorized"`) {
		t.Errorf("refused run %d %s", rec.Code, rec.Body)
	}
	w.fail = nil
	if rec := act("delete", target+`}`); rec.Code == 200 && strings.Contains(rec.Body.String(), `"done":1`) {
		t.Error("delete ran without a preview")
	}
	rec = act("delete", target+`,"preview":true}`)
	var plan struct{ Token string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &plan) != nil || plan.Token == "" {
		t.Fatalf("preview %d %s", rec.Code, rec.Body)
	}
	if rec := act("delete", target+`,"token":"`+plan.Token+`"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done":1`) {
		t.Errorf("delete %d %s", rec.Code, rec.Body)
	}
	if strings.Join(w.calls, ",") != "save "+saved.ID+",run "+saved.ID+",delete "+saved.ID {
		t.Errorf("calls %v", w.calls)
	}
	for _, bad := range []string{"user/../x", "root/0a1b2c3d", "user/0A1B2C3D"} {
		if rec := act("run", `{"target":"`+bad+`"}`); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}
