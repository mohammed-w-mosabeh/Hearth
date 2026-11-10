package modes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
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

type fired struct{ mode, by string }

type rig struct {
	db     *store.DB
	set    *Set
	dir    string
	now    time.Time
	conns  map[string]bool
	pct    float64
	disch  bool
	screen int
	next   time.Duration
	fired  []fired
	tr     *Triggers
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	write(t, dir, "away.toml", triggered)
	ps.Set.Load(context.Background())
	r := &rig{db: db, set: ps.Set, dir: dir, now: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC), conns: map[string]bool{},
		pct: 80, screen: 1, next: time.Hour}
	r.tr = NewTriggers(TriggerConfig{Set: ps.Set, DB: db, Clock: func() time.Time { return r.now },
		Sources: Sources{
			Next:        func(context.Context, string) (time.Time, error) { return r.now.Add(r.next), nil },
			Connections: func(context.Context) (map[string]bool, error) { return r.conns, nil },
			Battery:     func() (float64, bool, bool) { return r.pct, r.disch, true },
			Displays:    func() (int, error) { return r.screen, nil },
		},
		Activate: func(_ context.Context, mode, by, _ string) error {
			r.fired = append(r.fired, fired{mode, by})
			return nil
		}})
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

func (r *rig) check(t *testing.T, want ...string) {
	t.Helper()
	r.fired = nil
	r.tr.Check(context.Background())
	var got []string
	for _, f := range r.fired {
		got = append(got, f.by)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fired %v, want %v", got, want)
	}
}

// Nothing fires unless armed; armed, each fires on a change, never on what
// it finds when first looked at.
func TestTriggersFireOnChange(t *testing.T) {
	r := newRig(t)
	r.conns[homeUUID], r.pct, r.disch = true, 20, true
	r.check(t) // not armed: nothing, whatever the conditions
	r.arm(t, 0, 1, 2, 3, 4, 5)
	r.check(t) // armed while home Wi-Fi is up and the battery low: still nothing
	r.check(t)

	// Wi-Fi: down, then up again. Another connection, perhaps with the same
	// name, does nothing.
	r.conns = map[string]bool{otherUUID: true}
	r.check(t)
	r.conns[homeUUID] = true
	r.check(t, "wifi")
	r.check(t)

	// Battery: charging resets; falling below again while discharging fires.
	r.disch = false
	r.check(t)
	r.pct, r.disch = 25, true
	r.check(t, "battery")
	r.pct = 10
	r.check(t)
	r.pct = 35
	r.check(t)
	r.pct = 29
	r.check(t, "battery")

	// Displays: one more connected, then one fewer.
	r.screen = 2
	r.check(t, "display")
	r.screen = 1
	r.check(t, "display")
	if r.fired[0].mode != "away" {
		t.Fatal(r.fired)
	}

	// Schedule: when its time comes, then not again until the next.
	r.now = r.now.Add(time.Hour)
	r.check(t, "schedule")
	r.now = r.now.Add(30 * time.Minute)
	r.check(t)
	r.now = r.now.Add(31 * time.Minute)
	r.check(t, "schedule")
}

// A trigger armed for another version of the file never fires; arming it
// again starts afresh.
func TestTriggersNeedTheCurrentFile(t *testing.T) {
	r := newRig(t)
	r.arm(t, 1)
	r.check(t)
	write(t, r.dir, "away.toml", triggered+"\n# edited\n")
	r.set.Load(context.Background()) // disarms
	if a, _ := r.db.ModeArmings(context.Background()); len(a) != 0 {
		t.Fatalf("%+v", a)
	}
	// Even an arming planted for the old hash does nothing.
	if err := r.db.ArmModeTrigger(context.Background(), store.ModeArming{ModeID: "away", Trigger: 1, FileHash: Hash([]byte(triggered)), ArmedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	r.conns[homeUUID] = true
	r.check(t)
	r.conns[homeUUID] = false
	r.check(t)
	r.conns[homeUUID] = true
	r.check(t)
}

// The game trigger is the profile engine's, never this loop's.
func TestTriggersLeaveTheGameAlone(t *testing.T) {
	r := newRig(t)
	r.arm(t, 5)
	r.check(t)
	if list, _ := r.tr.armed(context.Background()); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

// Arming is a click in Hearth for the file the user was shown.
func TestArmAction(t *testing.T) {
	r := newRig(t)
	reg := actions.NewRegistry(r.db)
	RegisterArming(reg, r.set, r.db, func() time.Time { return r.now })
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
	if rec := post("disarm", good); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if a, _ := r.db.ModeArmings(context.Background()); len(a) != 0 {
		t.Fatalf("%+v", a)
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
