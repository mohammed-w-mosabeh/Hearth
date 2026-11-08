package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// host is a pretend machine behind fake actions.
type host struct {
	on      map[string]bool // running items, "kind:target"
	calls   []string
	failOn  map[string]bool // "kind/op target"
	games   []string
	gameErr error
	notes   []string
	batches []batchCall
	// settings are session settings by name; conns the active connections.
	settings map[string]string
	conns    map[string]bool
	// deferred are the stack steps automatic switches left for approval.
	deferred []deferCall
}

type deferCall struct {
	id    int64
	by    string
	items []Item
}

// Current implements Settings.
func (h *host) Current(_ context.Context, name string) (string, error) {
	v, ok := h.settings[name]
	if !ok {
		return "", errors.New("unsupported")
	}
	return v, nil
}

// Request implements Settings: each setting is the action "set/<name>".
func (h *host) Request(name, value string) (string, actions.Request, error) {
	if _, ok := h.settings[name]; !ok && name != "dnd" {
		return "", actions.Request{}, errors.New("no such setting")
	}
	return "set/" + name, actions.Request{Target: value, Params: map[string]string{}}, nil
}

// batchCall is one Batch call: its items and whether it could prompt.
type batchCall struct {
	items       []Item
	interactive bool
}

// batch is a pretend stack.batch over the host.
func (h *host) batch(_ context.Context, items []Item, interactive bool) []string {
	h.batches = append(h.batches, batchCall{items: slices.Clone(items), interactive: interactive})
	out := make([]string, len(items))
	for i, it := range items {
		if h.failOn["stack/"+it.Do+" "+it.Target] {
			out[i] = "error"
			continue
		}
		h.on[h.key(it)] = it.Do == DoStart
	}
	return out
}

func (h *host) key(it Item) string { return it.Kind + ":" + it.Target }

func (h *host) register(reg *actions.Registry) {
	add := func(kind, name string, risk actions.Risk, itemDo string) {
		reg.Register(actions.Action{Kind: kind, Name: name, Risk: risk,
			Validate: func(r actions.Request) error {
				if strings.HasPrefix(r.Target, "-") {
					return errors.New("bad target")
				}
				return nil
			},
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
				p := actions.Plan{Items: []actions.Item{{ID: r.Target, Name: r.Target}}}
				if kind == "unit" && name == "stop" {
					if r.Target == "user/hearthd.service" {
						return actions.Plan{Blocked: []actions.Blocked{{Item: p.Items[0], Reason: "self"}}}, nil
					}
					if r.Target == "system/dbus.service" {
						p.Risk, p.Confirm = actions.Destructive, "dbus"
					}
				}
				return p, nil
			},
			Run: func(_ context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
				call := kind + "/" + name + " " + r.Target
				h.calls = append(h.calls, call)
				if h.failOn[call] {
					return actions.Result{Failed: []actions.Blocked{{Item: actions.Item{ID: r.Target}, Reason: "error"}}}, nil
				}
				h.on[h.key(Item{Kind: kind, Target: r.Target})] = itemDo == DoStart
				return actions.Result{Done: 1}, nil
			}})
	}
	for _, kind := range []string{"unit", "container", "project", "stack"} {
		add(kind, "start", actions.Safe, DoStart)
		add(kind, "stop", actions.Reversible, DoStop)
	}
	add("app", "launch", actions.Safe, DoStart)
	for _, name := range []string{"dnd", "brightness", "wallpaper"} {
		reg.Register(actions.Action{Kind: "set", Name: name, Risk: actions.Safe,
			Validate: func(r actions.Request) error {
				if r.Target == "" || strings.HasPrefix(r.Target, "-") {
					return errors.New("bad value")
				}
				return nil
			},
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
				return actions.Plan{Items: []actions.Item{{ID: name, Name: r.Target}}}, nil
			},
			Run: func(_ context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
				call := "set/" + name + " " + r.Target
				h.calls = append(h.calls, call)
				if h.failOn[call] {
					return actions.Result{Failed: []actions.Blocked{{Item: actions.Item{ID: name}, Reason: "error"}}}, nil
				}
				v := r.Target
				if name == "brightness" { // the hardware rounds
					v = r.Target + "~"
				}
				h.settings[name] = v
				return actions.Result{Done: 1}, nil
			}})
	}
	for verb, on := range map[string]bool{"connect": true, "disconnect": false} {
		reg.Register(actions.Action{Kind: "network", Name: verb, Risk: actions.Reversible,
			Validate: func(actions.Request) error { return nil },
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
				return actions.Plan{Items: []actions.Item{{ID: r.Target, Name: r.Target}}}, nil
			},
			Run: func(_ context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
				h.calls = append(h.calls, "network/"+verb+" "+r.Target)
				h.conns[r.Target] = on
				return actions.Result{Done: 1}, nil
			}})
	}
}

// testStore is a Store the tests can also plant rows in, as if edited
// behind the API, and read the audit log of.
type testStore interface {
	Store
	AuditBetween(ctx context.Context, from, to time.Time, limit int) ([]store.AuditRow, error)
}

// newStore is the Store under test: the database, or mode files when
// TestProfilesOnModes runs the suite again (ROADMAP 10.2).
var newStore = func(_ *testing.T, db *store.DB) testStore { return db }

type fixture struct {
	h   *host
	m   *Manager
	db  testStore
	reg *actions.Registry
	mux *http.ServeMux
}

type muxer struct{ *http.ServeMux }

func (m muxer) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &host{on: map[string]bool{}, failOn: map[string]bool{}, settings: map[string]string{}, conns: map[string]bool{}}
	reg := actions.NewRegistry(db)
	h.register(reg)
	st := newStore(t, db)
	m := New(Config{Store: st, Actions: reg,
		Active: func(_ context.Context, it Item) (bool, error) {
			if it.Kind == KindConnection {
				return h.conns[it.Target], nil
			}
			return h.on[h.key(it)], nil
		},
		Settings: h,
		Defer: func(_ context.Context, p store.Profile, by string, items []Item) error {
			h.deferred = append(h.deferred, deferCall{p.ID, by, slices.Clone(items)})
			return nil
		},
		Playing: func() ([]string, error) { return h.games, h.gameErr },
		Notify:  func(_ context.Context, s, _ string) { h.notes = append(h.notes, s) },
		Batch:   h.batch,
	})
	m.Register(reg)
	mux := http.NewServeMux()
	m.HandleRegister(muxer{mux})
	mux.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	return &fixture{h: h, m: m, db: st, reg: reg, mux: mux}
}

func (f *fixture) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func (f *fixture) add(t *testing.T, name string, auto bool, items ...Item) int64 {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": name, "items": items, "auto_game": auto})
	rec := f.do(t, "POST", "/api/v1/profiles", string(b))
	if rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var p Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// activate previews and runs with the token, as the UI does.
func (f *fixture) activate(t *testing.T, id int64) (actions.Result, map[string]any) {
	t.Helper()
	target := fmt.Sprintf(`{"target":"%d"`, id)
	rec := f.do(t, "POST", "/api/v1/actions/profile/activate", target+`,"preview":true}`)
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var plan map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	body := target + "}"
	if tok, _ := plan["token"].(string); tok != "" {
		body = target + `,"token":"` + tok + `"}`
	}
	rec = f.do(t, "POST", "/api/v1/actions/profile/activate", body)
	if rec.Code != 200 {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body)
	}
	var res actions.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return res, plan
}

func (f *fixture) deactivate(t *testing.T, id int64) actions.Result {
	t.Helper()
	rec := f.do(t, "POST", "/api/v1/actions/profile/deactivate", fmt.Sprintf(`{"target":"%d"}`, id))
	if rec.Code != 200 {
		t.Fatalf("deactivate: %d %s", rec.Code, rec.Body)
	}
	var res actions.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return res
}

func (f *fixture) profile(t *testing.T, id int64) store.Profile {
	t.Helper()
	p, err := f.db.Profile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	userUnit = Item{Kind: KindUnit, Target: "user/syncnotes.service", Do: DoStop}
	sysUnit  = Item{Kind: KindUnit, Target: "system/ollama.service", Do: DoStop}
	box      = Item{Kind: KindContainer, Target: "postgres", Do: DoStart}
	app      = Item{Kind: KindApp, Target: "org.gnome.Calculator", Do: DoStart}
)

func TestSaveValidation(t *testing.T) {
	f := setup(t)
	for name, c := range map[string]struct {
		name  string
		auto  bool
		items []Item
	}{
		"no name":        {"  ", false, []Item{app}},
		"long name":      {strings.Repeat("x", 41), false, []Item{app}},
		"two lines":      {"a\nb", false, []Item{app}},
		"no items":       {"x", false, nil},
		"too many":       {"x", false, slices.Repeat([]Item{app}, 65)},
		"unknown kind":   {"x", false, []Item{{Kind: "exe", Target: "x", Do: DoStart}}},
		"unknown do":     {"x", false, []Item{{Kind: KindUnit, Target: "user/a.service", Do: "restart"}}},
		"app closing":    {"x", false, []Item{{Kind: KindApp, Target: "a", Do: DoStop}}},
		"bad target":     {"x", false, []Item{{Kind: KindContainer, Target: "-rm", Do: DoStop}}},
		"twice":          {"x", false, []Item{userUnit, {Kind: userUnit.Kind, Target: userUnit.Target, Do: DoStart}}},
		"critical":       {"x", false, []Item{{Kind: KindUnit, Target: "system/dbus.service", Do: DoStop}}},
		"hearth itself":  {"x", false, []Item{{Kind: KindUnit, Target: "user/hearthd.service", Do: DoStop}}},
		"auto system":    {"x", true, []Item{sysUnit}},
		"auto container": {"x", true, []Item{box}},
		"auto project":   {"x", true, []Item{{Kind: KindProject, Target: "web", Do: DoStop}}},
	} {
		b, _ := json.Marshal(map[string]any{"name": c.name, "items": c.items, "auto_game": c.auto})
		if rec := f.do(t, "POST", "/api/v1/profiles", string(b)); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := f.do(t, "POST", "/api/v1/profiles", `{"name":"x","extra":1}`); rec.Code != 400 {
		t.Errorf("extra field: %d", rec.Code)
	}
	f.add(t, "Dev", false, sysUnit, box)
	f.add(t, "Gaming", true, userUnit, app)
	if rec := f.do(t, "POST", "/api/v1/profiles", `{"name":"dev","items":[{"kind":"app","target":"a","do":"start"}]}`); rec.Code != 409 {
		t.Errorf("same name: %d", rec.Code)
	}
}

func TestActivateAndRestoreOnlyWhatChanged(t *testing.T) {
	f := setup(t)
	idle := Item{Kind: KindContainer, Target: "already-stopped", Do: DoStop}
	up := Item{Kind: KindUnit, Target: "user/already-up.service", Do: DoStart}
	f.h.on["unit:user/syncnotes.service"] = true
	f.h.on["unit:user/already-up.service"] = true
	id := f.add(t, "Mix", false, box, up, app, userUnit, idle)

	res, plan := f.activate(t, id)
	if plan["risk"] != "destructive" || plan["token"] == "" {
		t.Errorf("a stop needs the preview's token: %+v", plan)
	}
	if res.Done != 3 {
		t.Errorf("%+v", res)
	}
	// Stops come first; what was already as wanted is left alone.
	want := []string{"unit/stop user/syncnotes.service", "container/start postgres", "app/launch org.gnome.Calculator"}
	if !slices.Equal(f.h.calls, want) {
		t.Fatalf("%v", f.h.calls)
	}
	p := f.profile(t, id)
	if p.ActivatedAt.IsZero() || p.ActivatedBy != ByUser {
		t.Fatalf("%+v", p)
	}
	if rec := f.do(t, "DELETE", fmt.Sprintf("/api/v1/profiles/%d", id), ""); rec.Code != 409 {
		t.Errorf("deleting an active profile: %d", rec.Code)
	}
	if rec := f.do(t, "POST", "/api/v1/actions/profile/activate", fmt.Sprintf(`{"target":"%d","preview":true}`, id)); rec.Code != 409 {
		t.Errorf("activating twice: %d", rec.Code)
	}

	f.h.calls = nil
	f.deactivate(t, id)
	// In reverse, and the app stays open.
	if want := []string{"container/stop postgres", "unit/start user/syncnotes.service"}; !slices.Equal(f.h.calls, want) {
		t.Fatalf("%v", f.h.calls)
	}
	if !f.profile(t, id).ActivatedAt.IsZero() {
		t.Error("still active")
	}
	if rec := f.do(t, "POST", "/api/v1/actions/profile/deactivate", fmt.Sprintf(`{"target":"%d"}`, id)); rec.Code != 409 {
		t.Errorf("deactivating twice: %d", rec.Code)
	}

	rows, err := f.db.AuditBetween(context.Background(), time.UnixMilli(0), time.Now().Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Action+" "+r.Target] = true
	}
	for _, w := range []string{"unit.stop user/syncnotes.service", "container.start postgres", "unit.start user/syncnotes.service", fmt.Sprintf("profile.activate %d", id), fmt.Sprintf("profile.deactivate %d", id)} {
		if !seen[w] {
			t.Errorf("%q not audited: %v", w, seen)
		}
	}
}

func TestStartsAloneNeedNoToken(t *testing.T) {
	f := setup(t)
	id := f.add(t, "Dev", false, box)
	rec := f.do(t, "POST", "/api/v1/actions/profile/activate", fmt.Sprintf(`{"target":"%d"}`, id))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestStopWithoutPreviewIsRefused(t *testing.T) {
	f := setup(t)
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Quiet", false, userUnit)
	rec := f.do(t, "POST", "/api/v1/actions/profile/activate", fmt.Sprintf(`{"target":"%d"}`, id))
	if rec.Code != 409 || len(f.h.calls) != 0 {
		t.Fatalf("%d %v", rec.Code, f.h.calls)
	}
}

// A unit that became critical after saving is blocked, not run.
func TestRunTimeBlock(t *testing.T) {
	f := setup(t)
	f.h.on["unit:system/dbus.service"] = true
	id, err := f.db.AddProfile(context.Background(), store.Profile{Name: "old", Items: `[{"kind":"unit","target":"system/dbus.service","do":"stop"}]`})
	if err != nil {
		t.Fatal(err)
	}
	_, plan := f.activate(t, id)
	if blocked, _ := plan["blocked"].([]any); len(blocked) != 1 {
		t.Errorf("%+v", plan)
	}
	if len(f.h.calls) != 0 {
		t.Errorf("%v", f.h.calls)
	}
}

func TestPartialFailures(t *testing.T) {
	f := setup(t)
	f.h.on["unit:user/syncnotes.service"] = true
	f.h.failOn["container/start postgres"] = true
	id := f.add(t, "Mix", false, userUnit, box)
	res, _ := f.activate(t, id)
	if res.Done != 1 || len(res.Failed) != 1 {
		t.Fatalf("%+v", res)
	}
	var undo []Item
	_ = json.Unmarshal([]byte(f.profile(t, id).Undo), &undo)
	if len(undo) != 1 || undo[0].Target != userUnit.Target {
		t.Fatalf("only what changed is restored: %+v", undo)
	}

	// What cannot be restored stays to be tried again.
	f.h.failOn["unit/start user/syncnotes.service"] = true
	if res := f.deactivate(t, id); len(res.Failed) != 1 {
		t.Fatalf("%+v", res)
	}
	if f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("restoring failed but the profile is inactive")
	}
	delete(f.h.failOn, "unit/start user/syncnotes.service")
	f.deactivate(t, id)
	if !f.profile(t, id).ActivatedAt.IsZero() || !f.h.on["unit:user/syncnotes.service"] {
		t.Error("the retry did not restore")
	}

	// Nothing at all worked: not active.
	f.h.calls = nil
	f.h.failOn["unit/stop user/syncnotes.service"] = true
	id2 := f.add(t, "All", false, userUnit)
	f.activate(t, id2)
	if !f.profile(t, id2).ActivatedAt.IsZero() {
		t.Error("active though nothing changed")
	}
}

func TestGameAutoMode(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Gaming", true, userUnit)

	f.m.poll(ctx)
	if len(f.h.calls) != 0 {
		t.Fatalf("no game: %v", f.h.calls)
	}
	f.h.games = []string{"steam.620"}
	f.m.poll(ctx)
	if p := f.profile(t, id); p.ActivatedBy != ByGame || f.h.on["unit:user/syncnotes.service"] {
		t.Fatalf("%+v", p)
	}

	// The game goes on; the user changes their mind: it stays off.
	f.deactivate(t, id)
	f.m.poll(ctx)
	if !f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("activated again during the same game")
	}

	// The next game activates again; one poll without it is not the end.
	f.h.games = nil
	f.m.poll(ctx)
	f.m.poll(ctx)
	f.h.games = []string{"lutris"}
	f.m.poll(ctx)
	if f.profile(t, id).ActivatedBy != ByGame {
		t.Fatal("not activated for the next game")
	}
	f.h.games = nil
	f.m.poll(ctx)
	f.h.games = []string{"lutris"}
	f.m.poll(ctx) // the launcher between stages
	f.h.games = nil
	f.m.poll(ctx)
	if f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("restored on one missed poll")
	}
	f.m.poll(ctx)
	if !f.profile(t, id).ActivatedAt.IsZero() || !f.h.on["unit:user/syncnotes.service"] {
		t.Fatal("not restored after the game")
	}
	if len(f.h.notes) != 3 {
		t.Errorf("notifications %v", f.h.notes)
	}

	// Failing to look for games is not the game ending.
	f.h.games = []string{"steam.1"}
	f.m.poll(ctx)
	f.h.games, f.h.gameErr = nil, errors.New("no /proc")
	for range 4 {
		f.m.poll(ctx)
	}
	if f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("restored on an error")
	}
}

// A manual activation is not ended by a game ending.
func TestGameLeavesManualActivationAlone(t *testing.T) {
	f := setup(t)
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Gaming", true, userUnit)
	f.activate(t, id)
	for range 3 {
		f.m.poll(context.Background())
	}
	if f.profile(t, id).ActivatedBy != ByUser {
		t.Fatal("restored a profile the user activated")
	}
}

// hearthd stopped during a game: a new one restores what it left.
func TestRestoresAfterRestart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Gaming", true, userUnit)
	f.h.games = []string{"steam.620"}
	f.m.poll(ctx)
	if f.h.on["unit:user/syncnotes.service"] {
		t.Fatal("not stopped")
	}
	f.h.games = nil
	again := New(f.m.cfg)
	again.poll(ctx)
	again.poll(ctx)
	if !f.h.on["unit:user/syncnotes.service"] || !f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("not restored")
	}
}

// A failed restore stays for the user and is not retried every poll.
func TestAutoRestoreFailureStaysForTheUser(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Gaming", true, userUnit)
	f.h.games = []string{"steam.620"}
	f.m.poll(ctx)
	f.h.games = nil
	f.h.failOn["unit/start user/syncnotes.service"] = true
	f.h.calls = nil
	for range 6 {
		f.m.poll(ctx)
	}
	if len(f.h.calls) != 1 {
		t.Fatalf("%v", f.h.calls)
	}
	if p := f.profile(t, id); p.ActivatedBy != ByUser || p.ActivatedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
}

// Rows edited behind the API still cannot run unattended.
func TestAutoRefusesPasswordItems(t *testing.T) {
	f := setup(t)
	f.h.on["container:postgres"] = true
	if _, err := f.db.AddProfile(context.Background(), store.Profile{Name: "x", AutoGame: true,
		Items: `[{"kind":"container","target":"postgres","do":"stop"}]`}); err != nil {
		t.Fatal(err)
	}
	f.h.games = []string{"steam.1"}
	f.m.poll(context.Background())
	if len(f.h.calls) != 0 {
		t.Fatalf("%v", f.h.calls)
	}
}

func TestHandlers(t *testing.T) {
	f := setup(t)
	id := f.add(t, "Dev", false, box)
	rec := f.do(t, "GET", "/api/v1/profiles", "")
	var list struct {
		Profiles []Profile `json:"profiles"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Profiles) != 1 || list.Profiles[0].Items[0] != box {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, "PUT", fmt.Sprintf("/api/v1/profiles/%d", id), `{"name":"Work","items":[{"kind":"app","target":"a","do":"start"}]}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Work") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, "PUT", "/api/v1/profiles/99", `{"name":"x","items":[{"kind":"app","target":"a","do":"start"}]}`); rec.Code != 404 {
		t.Errorf("put: %d", rec.Code)
	}
	if rec := f.do(t, "PUT", "/api/v1/profiles/x", `{"name":"x","items":[{"kind":"app","target":"a","do":"start"}]}`); rec.Code != 400 {
		t.Errorf("put bad id: %d", rec.Code)
	}
	if rec := f.do(t, "POST", "/api/v1/actions/profile/activate", `{"target":"99","preview":true}`); rec.Code != 404 {
		t.Errorf("no such: %d", rec.Code)
	}
	if rec := f.do(t, "POST", "/api/v1/actions/profile/activate", `{"target":"x"}`); rec.Code != 400 {
		t.Errorf("bad target: %d", rec.Code)
	}
	if rec := f.do(t, "DELETE", "/api/v1/profiles/99", ""); rec.Code != 404 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := f.do(t, "DELETE", fmt.Sprintf("/api/v1/profiles/%d", id), ""); rec.Code != 204 {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
}
