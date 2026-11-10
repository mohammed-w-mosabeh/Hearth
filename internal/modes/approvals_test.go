package modes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const withStacks = `name = "Battery"
[session]
do_not_disturb = true
[[stack]]
name = "photos"
do = "stop"
[[stack]]
name = "notes"
do = "start"
`

type approvalRig struct {
	db      *store.DB
	set     *Set
	dir     string
	now     time.Time
	applied [][]Item
	ap      *Approvals
	mux     *http.ServeMux
}

func newApprovalRig(t *testing.T) *approvalRig {
	t.Helper()
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	write(t, dir, "battery.toml", withStacks)
	ps.Set.Load(context.Background())
	r := &approvalRig{db: db, set: ps.Set, dir: dir, now: time.UnixMilli(1793359297763)}
	r.ap = NewApprovals(ApprovalConfig{Set: ps.Set, DB: db, Clock: func() time.Time { return r.now },
		Apply: func(_ context.Context, mode string, items []Item) (actions.Result, error) {
			r.applied = append(r.applied, items)
			return actions.Result{Done: len(items), Failed: []actions.Blocked{}}, nil
		}})
	reg := actions.NewRegistry(db)
	r.ap.Register(reg)
	r.mux = http.NewServeMux()
	r.mux.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	return r
}

// switchOn records the mode as switched on by a trigger, as the engine does.
func (r *approvalRig) switchOn(t *testing.T) Mode {
	t.Helper()
	m, _ := r.set.Mode("battery")
	if err := r.db.SetActiveMode(context.Background(), store.ActiveMode{ModeID: "battery", FileHash: m.Hash, Since: r.now, By: "battery", Snapshot: `{"undo":[]}`}); err != nil {
		t.Fatal(err)
	}
	return m
}

func (r *approvalRig) deferStacks(t *testing.T, m Mode) int64 {
	t.Helper()
	items := []Item{{Kind: KindStack, Target: "photos", Do: DoStop}, {Kind: KindStack, Target: "notes", Do: DoStart}}
	if err := r.ap.Defer(context.Background(), m.ID, m.Hash, "battery", items); err != nil {
		t.Fatal(err)
	}
	list, err := r.ap.Pending(context.Background())
	if err != nil || len(list) == 0 {
		t.Fatalf("%v %+v", err, list)
	}
	return list[len(list)-1].ID
}

func (r *approvalRig) post(action, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/actions/mode/"+action, strings.NewReader(body)))
	return rec
}

func TestApprove(t *testing.T) {
	r := newApprovalRig(t)
	m := r.switchOn(t)
	id := r.deferStacks(t, m)
	list, _ := r.ap.Pending(context.Background())
	if len(list) != 1 || list[0].Name != "Battery" || list[0].Reason != "battery" || len(list[0].Items) != 2 {
		t.Fatalf("%+v", list)
	}
	target := `{"target":"` + itoa(id) + `"`
	// Stopping needs the preview's token: no approval by a bare POST.
	if rec := r.post("approve", target+`}`); rec.Code == 200 {
		t.Fatalf("approved without a preview: %s", rec.Body)
	}
	rec := r.post("approve", target+`,"preview":true}`)
	var plan map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	tok, _ := plan["token"].(string)
	if rec.Code != 200 || tok == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := r.post("approve", target+`,"token":"`+tok+`"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.applied) != 1 || len(r.applied[0]) != 2 || r.applied[0][0].Target != "photos" {
		t.Fatalf("%+v", r.applied)
	}
	// Decided once: neither approve nor dismiss runs again.
	if rec := r.post("approve", target+`,"preview":true}`); rec.Code != 409 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := r.post("dismiss", target+`}`); rec.Code != 409 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if list, _ := r.ap.Pending(context.Background()); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestDismiss(t *testing.T) {
	r := newApprovalRig(t)
	id := r.deferStacks(t, r.switchOn(t))
	if rec := r.post("dismiss", `{"target":"`+itoa(id)+`"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.applied) != 0 {
		t.Fatal("dismissing ran something")
	}
	a, _ := r.db.ModeApproval(context.Background(), id)
	if a.State != store.ApprovalDismissed {
		t.Fatalf("%+v", a)
	}
}

// An approval that can no longer be meant expires: the mode was reverted,
// its file changed, or it waited too long.
func TestApprovalsExpire(t *testing.T) {
	for name, stale := range map[string]func(*testing.T, *approvalRig){
		"reverted": func(t *testing.T, r *approvalRig) { _ = r.db.ClearActiveMode(context.Background(), "battery") },
		"file changed": func(t *testing.T, r *approvalRig) {
			write(t, r.dir, "battery.toml", withStacks+"# edited\n")
			r.set.Load(context.Background())
		},
		"too old": func(t *testing.T, r *approvalRig) { r.now = r.now.Add(ApprovalTTL + time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newApprovalRig(t)
			id := r.deferStacks(t, r.switchOn(t))
			stale(t, r)
			if rec := r.post("approve", `{"target":"`+itoa(id)+`","preview":true}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "expired") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			a, _ := r.db.ModeApproval(context.Background(), id)
			if a.State != store.ApprovalExpired || len(r.applied) != 0 {
				t.Fatalf("%+v %+v", a, r.applied)
			}
		})
	}
}

func TestDeferOnlyStacks(t *testing.T) {
	r := newApprovalRig(t)
	m := r.switchOn(t)
	for _, bad := range [][]Item{{{Kind: KindUnit, Target: "system/x.service", Do: DoStop}}, {{Kind: KindStack, Target: "x", Do: "restart"}}} {
		if err := r.ap.Defer(context.Background(), m.ID, m.Hash, "battery", bad); err == nil {
			t.Errorf("deferred %+v", bad)
		}
	}
	if rec := r.post("approve", `{"target":"x"}`); rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := r.post("approve", `{"target":"99","preview":true}`); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
