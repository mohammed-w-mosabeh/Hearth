package modes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	homeUUID  = "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"
	otherUUID = "11111111-2222-3333-4444-555555555555"
)

const triggered = `name = "Away"
[session]
do_not_disturb = true

[[trigger]]
kind = "schedule"
on_calendar = "Mon..Fri 09:00"

[[trigger]]
kind = "wifi"
connection = "` + homeUUID + `"

[[trigger]]
kind = "battery"
below = 30

[[trigger]]
kind = "display"
event = "connect"

[[trigger]]
kind = "display"
event = "disconnect"

[[trigger]]
kind = "game"
`

type rig struct {
	db    *store.DB
	set   *Set
	dir   string
	now   time.Time
	sync  *Syncer
	syncs int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	write(t, dir, "away.toml", triggered)
	ps.Set.Load(context.Background())
	r := &rig{db: db, set: ps.Set, dir: dir, now: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)}
	r.sync = &Syncer{DB: db, Set: ps.Set, Changed: func() { r.syncs++ }}
	return r
}

func (r *rig) arm(t *testing.T, idx ...int) {
	t.Helper()
	m, _ := r.set.Mode("away")
	for _, i := range idx {
		if err := r.db.ArmModeTrigger(context.Background(), store.ModeArming{ModeID: "away", Trigger: i, FileHash: m.Hash, ArmedAt: r.now}); err != nil {
			t.Fatal(err)
		}
	}
}

// modeAutos are the mode triggers' automations, by origin.
func (r *rig) modeAutos(t *testing.T) map[string]store.Automation {
	t.Helper()
	all, err := r.db.Automations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Automation{}
	for _, a := range all {
		if IsTriggerOrigin(a.Origin) {
			out[a.Origin] = a
		}
	}
	return out
}

// Every trigger kind of a mode file becomes a valid automation running
// mode/activate for that file's hash.
func TestTriggerSpecs(t *testing.T) {
	r := newRig(t)
	m, _ := r.set.Mode("away")
	want := []string{"schedule", "wifi", "metric", "display", "display", "app"}
	for i := range m.Triggers {
		s, err := TriggerSpec(m, i)
		if err != nil {
			t.Fatal(i, err)
		}
		raw, _ := json.Marshal(s)
		got, err := spec.Parse(raw)
		if err != nil {
			t.Fatalf("trigger %d: %v", i, err)
		}
		if got.Trigger.Kind != want[i] || len(got.Then) != 1 || got.Then[0].Action != "mode/activate" ||
			got.Then[0].Target != "away" || got.Then[0].Params["hash"] != m.Hash || got.Then[0].Params["trigger"] != strconv.Itoa(i) {
			t.Fatalf("trigger %d: %+v", i, got)
		}
	}
	s, _ := TriggerSpec(m, 2)
	if s.Trigger.Metric != "battery.discharging" || s.Trigger.Op != "<" || s.Trigger.Threshold != 30 {
		t.Fatalf("%+v", s.Trigger)
	}
	for i, ev := range map[int]string{3: "connected", 4: "disconnected"} {
		if s, _ := TriggerSpec(m, i); s.Trigger.Event != ev {
			t.Fatalf("%d: %+v", i, s.Trigger)
		}
	}
	if s, _ := TriggerSpec(m, 5); !s.Trigger.Game || s.Trigger.State != "started" {
		t.Fatalf("%+v", s.Trigger)
	}
	if _, err := TriggerSpec(m, 6); err == nil {
		t.Fatal("no such trigger")
	}
}

// The automations follow what is armed for the current file: arming adds,
// disarming deletes, a changed file deletes, and a sync with nothing to do
// changes nothing.
func TestSyncFollowsArming(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.sync.Now(ctx)
	if len(r.modeAutos(t)) != 0 || r.syncs != 0 {
		t.Fatal("nothing armed, nothing made")
	}
	m, _ := r.set.Mode("away")
	r.arm(t, 0, 5)
	r.sync.Now(ctx)
	got := r.modeAutos(t)
	if len(got) != 2 || r.syncs != 1 {
		t.Fatalf("%d syncs: %+v", r.syncs, got)
	}
	a, ok := got[Origin("away", 5, m.Hash)]
	if !ok || !a.Enabled || a.Name != "Away: game trigger 6" {
		t.Fatalf("%+v", got)
	}
	r.sync.Now(ctx)
	if r.syncs != 1 || len(r.modeAutos(t)) != 2 {
		t.Fatal("idempotent")
	}
	if err := r.db.DisarmModeTrigger(ctx, "away", 0); err != nil {
		t.Fatal(err)
	}
	r.sync.Now(ctx)
	if got := r.modeAutos(t); len(got) != 1 || r.syncs != 2 {
		t.Fatalf("%+v", got)
	}
	// The file changes: still armed in the table until the reload disarms
	// it, its automation goes at once.
	write(t, r.dir, "away.toml", triggered+"\n# edited\n")
	r.set.Load(ctx)
	r.sync.Now(ctx)
	if got := r.modeAutos(t); len(got) != 0 {
		t.Fatalf("a changed file keeps %+v", got)
	}
}

// mode/activate switches the mode on as the trigger did, and refuses a
// file that changed since it was armed.
func TestActivateAction(t *testing.T) {
	r := newRig(t)
	reg := actions.NewRegistry(r.db)
	var got []string
	RegisterActivate(reg, r.set, func(_ context.Context, id, by, why string) error {
		got = append(got, id+"|"+by+"|"+why)
		return nil
	})
	m, _ := r.set.Mode("away")
	ctx := context.Background()
	if a, _ := reg.Lookup("mode/activate"); a.Authority != actions.AuthorityNone || a.Risk != actions.Reversible {
		t.Fatalf("%+v", a)
	}
	u, err := reg.RunUnattended(ctx, "mode/activate", actions.Request{Target: "away", Params: map[string]string{"trigger": "2", "hash": m.Hash}}, "automation:1")
	if err != nil || u.Result.Done != 1 {
		t.Fatal(u, err)
	}
	if len(got) != 1 || got[0] != "away|battery|the battery fell below 30%" {
		t.Fatalf("%q", got)
	}
	_, err = reg.RunUnattended(ctx, "mode/activate", actions.Request{Target: "away", Params: map[string]string{"trigger": "2", "hash": strings.Repeat("0", 64)}}, "automation:1")
	if err == nil || len(got) != 1 {
		t.Fatal("a stale hash must be refused", err)
	}
}

func TestArmAction(t *testing.T) {
	r := newRig(t)
	reg := actions.NewRegistry(r.db)
	RegisterArming(reg, r.set, r.sync.Arming(r.db), func() time.Time { return r.now })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	post := func(action, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/actions/mode/"+action, strings.NewReader(body)))
		return rec
	}
	m, _ := r.set.Mode("away")
	good := `{"target":"away","params":{"trigger":"1","hash":"` + m.Hash + `"}}`
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"stale hash":  {`{"target":"away","params":{"trigger":"1","hash":"` + strings.Repeat("0", 64) + `"}}`, 409},
		"no trigger":  {`{"target":"away","params":{"trigger":"7","hash":"` + m.Hash + `"}}`, 404},
		"no mode":     {`{"target":"gone","params":{"trigger":"1","hash":"` + m.Hash + `"}}`, 404},
		"bad id":      {`{"target":"../x","params":{"trigger":"1","hash":"` + m.Hash + `"}}`, 400},
		"bad index":   {`{"target":"away","params":{"trigger":"-1","hash":"` + m.Hash + `"}}`, 400},
		"extra param": {`{"target":"away","params":{"trigger":"1","hash":"` + m.Hash + `","by":"file"}}`, 400},
	} {
		if rec := post("arm", c.body); rec.Code != c.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if a, _ := r.db.ModeArmings(context.Background()); len(a) != 0 {
		t.Fatalf("armed by a refused request: %+v", a)
	}
	if rec := post("arm", good); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a, _ := r.db.ModeArmings(context.Background())
	if len(a) != 1 || a[0].Trigger != 1 || a[0].FileHash != m.Hash {
		t.Fatalf("%+v", a)
	}
	if _, ok := r.modeAutos(t)[Origin("away", 1, m.Hash)]; !ok {
		t.Fatal("arming makes the automation at once")
	}
	if rec := post("disarm", good); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if a, _ := r.db.ModeArmings(context.Background()); len(a) != 0 {
		t.Fatalf("%+v", a)
	}
	if len(r.modeAutos(t)) != 0 {
		t.Fatal("disarming deletes the automation at once")
	}
	rows, _ := r.db.AuditBetween(context.Background(), time.UnixMilli(0), time.Now().Add(time.Hour), 10)
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Action] = true
	}
	if !seen["mode.arm"] || !seen["mode.disarm"] {
		t.Fatalf("%+v", rows)
	}
}
