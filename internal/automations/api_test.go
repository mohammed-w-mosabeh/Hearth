package automations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func rig(t *testing.T) (*httptest.Server, *store.DB, *int) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	changed := 0
	a := &API{Store: db, Changed: func() { changed++ },
		Check: NewChecker(CheckDeps{Known: func(action string) bool { return action == "power/profile" || action == "notify/send" },
			Calendar: func(_ context.Context, expr string) error {
				if expr == "nonsense" {
					return errors.New("systemd does not understand this schedule")
				}
				return nil
			}})}
	m := mux{http.NewServeMux()}
	a.Register(m)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv, db, &changed
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

const heat = `{"name":"Heat","enabled":true,"spec":{"trigger":{"kind":"metric","metric":"cpu.temp","op":">","threshold":90,"for_s":60},
  "then":[{"step":"action","action":"power/profile","target":"power-saver"}],"options":{"cooldown_s":300,"max_per_hour":6}}}`

func TestAPI(t *testing.T) {
	srv, db, changed := rig(t)
	code, out := do(t, srv, "POST", "/api/v1/automations", heat)
	if code != 200 || out["origin"] != "ui" {
		t.Fatalf("%d %v", code, out)
	}
	id := int64(out["id"].(float64))
	for name, body := range map[string]string{
		"unknown action":  strings.Replace(heat, "power/profile", "unit/frobnicate", 1),
		"denied action":   strings.Replace(heat, "power/profile", "uninstall/remove", 1),
		"bad schedule":    `{"name":"x","enabled":true,"spec":{"trigger":{"kind":"schedule","calendar":"nonsense"},"then":[{"step":"wait","seconds":1}]}}`,
		"unknown field":   strings.Replace(heat, `"enabled":true`, `"enabled":true,"script":"id"`, 1),
		"empty name":      strings.Replace(heat, `"Heat"`, `""`, 1),
		"control in name": strings.Replace(heat, `"Heat"`, `"a\u0007b"`, 1),
		"oversized":       `{"name":"x","enabled":true,"spec":{"pad":"` + strings.Repeat("a", 40<<10) + `"}}`,
	} {
		if code, _ := do(t, srv, "POST", "/api/v1/automations", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	code, out = do(t, srv, "PUT", "/api/v1/automations/"+itoa(id), strings.Replace(heat, "Heat", "Heat 2", 1))
	if code != 200 || out["name"] != "Heat 2" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := do(t, srv, "PUT", "/api/v1/automations/"+itoa(id)+"/enabled", `{"enabled":false}`); code != 200 {
		t.Fatal(code)
	}
	// Export, then import: the copy starts disabled.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/automations/export", nil)
	res, _ := http.DefaultClient.Do(req)
	var file spec.File
	_ = json.NewDecoder(res.Body).Decode(&file)
	_ = res.Body.Close()
	file.Automations[0].Enabled = true
	raw, _ := json.Marshal(file)
	code, out = do(t, srv, "POST", "/api/v1/automations/import", string(raw))
	if code != 200 || len(out["created"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	all, _ := db.Automations(context.Background())
	if len(all) != 2 || all[1].Enabled || all[1].Origin != "import" {
		t.Fatalf("%+v", all)
	}
	// A malicious import stores nothing.
	for _, bad := range []string{
		`{"version":1,"automations":[{"name":"x","spec":{"trigger":{"kind":"login"},"then":[{"step":"action","action":"sh/c","target":"rm -rf ~"}]}}]}`,
		`{"version":1,"automations":[{"name":"x","spec":{"trigger":{"kind":"login","exec":"/bin/sh"},"then":[{"step":"wait","seconds":1}]}}]}`,
		`{"version":1,"automations":[{"name":"x","spec":{"trigger":{"kind":"login"},"then":[` + strings.Repeat(`{"step":"check","what":"unit_state","target":"x","expect":"active","then":[`, 50) + `]}}]}`,
		strings.Repeat("[", 100000),
	} {
		if code, _ := do(t, srv, "POST", "/api/v1/automations/import", bad); code != http.StatusBadRequest {
			t.Errorf("import accepted: %.80s", bad)
		}
	}
	if all, _ := db.Automations(context.Background()); len(all) != 2 {
		t.Fatalf("a refused import stored %d", len(all))
	}
	// A mode's automation is the mode file's alone.
	_, _ = db.CreateAutomations(context.Background(), []store.Automation{{Name: "m", Spec: all[0].Spec, Origin: "mode:evening.toml#0@abc"}})
	all, _ = db.Automations(context.Background())
	mid := itoa(all[2].ID)
	for _, req := range [][2]string{{"PUT", "/api/v1/automations/" + mid}, {"PUT", "/api/v1/automations/" + mid + "/enabled"}, {"DELETE", "/api/v1/automations/" + mid}} {
		body := heat
		if strings.HasSuffix(req[1], "/enabled") {
			body = `{"enabled":false}`
		}
		if code, _ := do(t, srv, req[0], req[1], body); code != http.StatusConflict {
			t.Errorf("%v: %d", req, code)
		}
	}
	if code, _ := do(t, srv, "DELETE", "/api/v1/automations/"+itoa(id), ""); code != 200 {
		t.Fatal(code)
	}
	if code, _ := do(t, srv, "DELETE", "/api/v1/automations/"+itoa(id), ""); code != 404 {
		t.Fatal(code)
	}
	if *changed != 5 {
		t.Fatalf("changed %d times", *changed)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

type fakePause struct{ on bool }

func (f *fakePause) Paused(context.Context) bool { return f.on }
func (f *fakePause) SetPaused(_ context.Context, on bool, _ string) error {
	f.on = on
	return nil
}

func TestPauseRoutes(t *testing.T) {
	p := &fakePause{}
	a := &API{Engine: p}
	m := mux{http.NewServeMux()}
	a.Register(m)
	srv := httptest.NewServer(m)
	defer srv.Close()
	if code, out := do(t, srv, "PUT", "/api/v1/automations/pause", `{"paused":true}`); code != 200 || out["paused"] != true || !p.on {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := do(t, srv, "GET", "/api/v1/automations/pause", ""); code != 200 || out["paused"] != true {
		t.Fatalf("%d %v", code, out)
	}
	for _, bad := range []string{`{}`, `{"paused":"yes"}`, `{"paused":true,"x":1}`} {
		if code, _ := do(t, srv, "PUT", "/api/v1/automations/pause", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

type fakeApprovals struct{ err error }

func (f fakeApprovals) Approvals(context.Context) ([]ApprovalView, error) {
	return []ApprovalView{{ID: 1, Name: "<b>heal</b>"}}, f.err
}

func TestApprovalsRoute(t *testing.T) {
	a := &API{Approvals: fakeApprovals{}, ModeApprovals: func(context.Context) (any, error) { return []map[string]int{{"id": 7}}, nil }}
	m := mux{http.NewServeMux()}
	a.Register(m)
	srv := httptest.NewServer(m)
	defer srv.Close()
	code, out := do(t, srv, "GET", "/api/v1/automations/approvals", "")
	autos, _ := out["automations"].([]any)
	modes, _ := out["modes"].([]any)
	if code != 200 || len(autos) != 1 || len(modes) != 1 || autos[0].(map[string]any)["name"] != "<b>heal</b>" {
		t.Fatalf("%d %v", code, out)
	}
	a2 := &API{}
	m2 := mux{http.NewServeMux()}
	a2.Register(m2)
	srv2 := httptest.NewServer(m2)
	defer srv2.Close()
	if code, out := do(t, srv2, "GET", "/api/v1/automations/approvals", ""); code != 200 || len(out["automations"].([]any)) != 0 {
		t.Fatalf("%d %v", code, out)
	}
	a3 := &API{Approvals: fakeApprovals{err: errors.New("db")}}
	m3 := mux{http.NewServeMux()}
	a3.Register(m3)
	srv3 := httptest.NewServer(m3)
	defer srv3.Close()
	if code, _ := do(t, srv3, "GET", "/api/v1/automations/approvals", ""); code != 500 {
		t.Fatal(code)
	}
}

// Only arming a mode's trigger makes an automation that switches a mode
// on: the editor and an import refuse mode/activate, even in a branch.
func TestModeActivateOnlyFromArming(t *testing.T) {
	srv, _, _ := rig(t)
	step := `{"step":"action","action":"mode/activate","target":"away","params":{"trigger":"0","hash":"` + strings.Repeat("a", 64) + `"}}`
	direct := `{"name":"Sneaky","enabled":true,"spec":{"trigger":{"kind":"login"},"then":[` + step + `]}}`
	branch := `{"name":"Sneaky","enabled":true,"spec":{"trigger":{"kind":"login"},"then":[{"step":"check","what":"unit_state","target":"user/x.service","expect":"active","then":[` + step + `]}]}}`
	for _, body := range []string{direct, branch} {
		code, out := do(t, srv, "POST", "/api/v1/automations", body)
		if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "Modes page") {
			t.Fatalf("%d %v", code, out)
		}
	}
	imp := `{"version":1,"automations":[{"name":"Sneaky","enabled":false,"spec":{"trigger":{"kind":"login"},"then":[` + step + `]}}]}`
	if code, out := do(t, srv, "POST", "/api/v1/automations/import", imp); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "Modes page") {
		t.Fatalf("%d %v", code, out)
	}
}

type fakeTester struct{ ids []int64 }

func (f *fakeTester) Test(_ context.Context, id int64, check func(context.Context, spec.Spec) error) error {
	if check == nil {
		return errors.New("the save-time check must be passed on")
	}
	f.ids = append(f.ids, id)
	return nil
}

// Test now and the dry run: a mode's trigger is not tested here, the dry
// run takes the editor's spec through the save-time checks and stores
// nothing.
func TestTestAndDryRunRoutes(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tester := &fakeTester{}
	var dry []spec.Spec
	a := &API{Store: db, Tester: tester,
		Check: NewChecker(CheckDeps{Known: func(action string) bool { return action == "power/profile" }}),
		DryRun: func(_ context.Context, id int64, s spec.Spec) (DryRun, error) {
			dry = append(dry, s)
			return DryRun{Total: int(id)}, nil
		}}
	m := mux{http.NewServeMux()}
	a.Register(m)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	login := json.RawMessage(`{"trigger":{"kind":"login"},"then":[{"step":"action","action":"power/profile","target":"a"}],"options":{"cooldown_s":300,"max_per_hour":6}}`)
	ids, _ := db.CreateAutomations(ctx, []store.Automation{{Name: "a", Spec: login, Origin: "ui"}, {Name: "m", Spec: login, Origin: "mode:x#0@" + strings.Repeat("a", 64)}})
	if code, _ := do(t, srv, "POST", "/api/v1/automations/"+itoa(ids[0])+"/test", ""); code != http.StatusAccepted || len(tester.ids) != 1 {
		t.Fatal(code, tester.ids)
	}
	if code, _ := do(t, srv, "POST", "/api/v1/automations/"+itoa(ids[1])+"/test", ""); code != http.StatusConflict || len(tester.ids) != 1 {
		t.Fatal("a mode's trigger tested", code)
	}
	if code, _ := do(t, srv, "POST", "/api/v1/automations/99/test", ""); code != http.StatusNotFound {
		t.Fatal(code)
	}
	code, out := do(t, srv, "POST", "/api/v1/automations/dry-run", `{"id":3,"spec":`+string(login)+`}`)
	if code != 200 || out["total"] != float64(3) || len(dry) != 1 {
		t.Fatal(code, out)
	}
	for _, bad := range []string{
		`{"spec":{"trigger":{"kind":"shell"},"then":[]}}`,
		`{"spec":{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unknown/thing","target":"a"}]}}`,
		`{"spec":{"trigger":{"kind":"login"},"then":[{"step":"action","action":"mode/activate","target":"x","params":{"trigger":"0","hash":"` + strings.Repeat("a", 64) + `"}}]}}`,
		`{"spec":` + string(login) + `,"name":"extra"}`,
		`{"id":-1,"spec":` + string(login) + `}`,
	} {
		if code, _ := do(t, srv, "POST", "/api/v1/automations/dry-run", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if len(dry) != 1 {
		t.Fatal("a refused spec was replayed")
	}
	if list, _ := db.Automations(ctx); len(list) != 2 {
		t.Fatal("a dry run stored something")
	}
}

// The editor's routes: the actions it may offer, live checks with each
// step's outcome, the list's last run and waiting approvals, and a
// template's origin kept from creation on.
func TestEditorRoutes(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := actions.NewRegistry(db)
	for _, k := range []string{"power/profile", "mode/activate", "mode/arm", "hearth/import"} {
		kind, name, _ := strings.Cut(k, "/")
		reg.Register(actions.Action{Kind: kind, Name: name, Risk: actions.Safe, Validate: func(actions.Request) error { return nil },
			Plan: func(context.Context, actions.Request) (actions.Plan, error) { return actions.Plan{}, nil },
			Run: func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
				return actions.Result{}, nil
			}})
	}
	a := &API{Store: db, Actions: func() []ActionInfo { return Offered(reg) },
		Check: NewChecker(CheckDeps{Known: func(action string) bool { return action == "power/profile" }}),
		Steps: func(context.Context, spec.Spec) []DryStep { return []DryStep{{Step: "action", Outcome: DryUnattended}} },
		Next:  func() map[int64]time.Time { return map[int64]time.Time{1: time.UnixMilli(5000)} }}
	m := mux{http.NewServeMux()}
	a.Register(m)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)

	code, out := do(t, srv, "GET", "/api/v1/automations/actions", "")
	if code != 200 || len(out["actions"].([]any)) != 1 || out["actions"].([]any)[0].(map[string]any)["key"] != "power/profile" {
		t.Fatalf("%d %v", code, out)
	}
	good := `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"power/profile","target":"a"}]}`
	if code, out := do(t, srv, "POST", "/api/v1/automations/check", `{"spec":`+good+`}`); code != 200 || out["error"] != nil || len(out["steps"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := do(t, srv, "POST", "/api/v1/automations/check", `{"spec":{"trigger":{"kind":"login"},"then":[]}}`); code != 200 || out["error"] == "" {
		t.Fatalf("%d %v", code, out)
	}
	code, out = do(t, srv, "POST", "/api/v1/automations", `{"name":"Heat","enabled":true,"origin":"template:heat","spec":`+good+`}`)
	if code != 200 || out["origin"] != "template:heat" {
		t.Fatalf("%d %v", code, out)
	}
	id := int64(out["id"].(float64))
	if code, out := do(t, srv, "PUT", "/api/v1/automations/"+itoa(id), `{"name":"Heat 2","enabled":true,"origin":"template:other","spec":`+good+`}`); code != 200 || out["origin"] != "template:heat" {
		t.Fatalf("the origin changed: %d %v", code, out)
	}
	if code, _ := do(t, srv, "POST", "/api/v1/automations", `{"name":"x","enabled":true,"origin":"mode:x#0@abc","spec":`+good+`}`); code != 400 {
		t.Fatal("a mode origin was accepted from the editor", code)
	}
	if _, err := db.SaveAutomationRun(context.Background(), store.AutomationRun{AutomationID: id, Started: 7, State: RunDone, Record: json.RawMessage(`{"by":"test","steps":[]}`)}); err != nil {
		t.Fatal(err)
	}
	_, out = do(t, srv, "GET", "/api/v1/automations", "")
	v := out["automations"].([]any)[0].(map[string]any)
	if last := v["last"].(map[string]any); last["state"] != RunDone || last["by"] != "test" || v["next"] != float64(5000) {
		t.Fatalf("%v", v)
	}
}
