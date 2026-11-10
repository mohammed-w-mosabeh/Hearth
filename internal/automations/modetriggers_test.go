package automations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const batteryLow = `{"trigger":{"kind":"metric","metric":"battery.discharging","op":"<","threshold":30},
	"then":[{"step":"action","action":"power/profile","target":"low"}],"options":{"cooldown_s":60,"max_per_hour":60}}`

// A mode's battery trigger fires when the battery falls below its level,
// never on what it finds when armed or at start (10.6); any other metric
// trigger keeps the alert engine's way and fires on what it finds.
func TestModeBatteryFiresOnChangeOnly(t *testing.T) {
	g := newEngineRig(t)
	s, _ := spec.Parse([]byte(batteryLow))
	b, _ := json.Marshal(s)
	g.st.autos = append(g.st.autos, store.Automation{ID: 1, Name: "Away: battery", Enabled: true, Spec: b, Origin: "mode:away#2@abc"})
	if err := g.e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.observe("battery.discharging", map[string]float64{"BAT0": 20})
	if g.ranList() != "" {
		t.Fatalf("fired on what it first found: %q", g.ranList())
	}
	g.observe("battery.discharging", map[string]float64{}) // plugged in
	g.now = g.now.Add(time.Minute)
	g.observe("battery.discharging", map[string]float64{"BAT0": 25}) // unplugged below the level
	if g.ranList() != "power/profile low" {
		t.Fatalf("%q", g.ranList())
	}

	h := newEngineRig(t)
	h.add("ui", batteryLow)
	h.observe("battery.discharging", map[string]float64{"BAT0": 20})
	if h.ranList() != "power/profile low" {
		t.Fatalf("a metric trigger made in the editor fires on what it finds: %q", h.ranList())
	}
}

// End to end: arming a display trigger makes its automation; the engine
// fires it on a change, mode/activate runs unattended and switches the mode
// on by "display"; a changed file stops it.
func TestArmedModeTriggerRuns(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dir := t.TempDir()
	file := filepath.Join(dir, "desk.toml")
	writeFile(t, file, "name = \"Desk\"\n[session]\ndo_not_disturb = true\n[[trigger]]\nkind = \"display\"\nevent = \"connect\"\n")
	set := modes.NewSet(modes.Config{Dir: dir, Store: db})
	set.Load(ctx)
	m, ok := set.Mode("desk")
	if !ok {
		t.Fatal("mode not read", set.Errors())
	}
	reg := actions.NewRegistry(db)
	var got []string
	modes.RegisterActivate(reg, set, func(_ context.Context, id, by, why string) error {
		got = append(got, id+"|"+by+"|"+why)
		return nil
	})
	screens := 1
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	e := NewEngine(Config{Store: db, Registry: reg, Clock: func() time.Time { return now },
		Sources: modes.Sources{Displays: func() (int, error) { return screens, nil }},
		Sleep:   func(context.Context, time.Duration) error { return nil }})
	sync := &modes.Syncer{DB: db, Set: set, Changed: func() { _ = e.Load(ctx) }}
	if err := db.ArmModeTrigger(ctx, store.ModeArming{ModeID: "desk", Trigger: 0, FileHash: m.Hash, ArmedAt: now}); err != nil {
		t.Fatal(err)
	}
	sync.Now(ctx)
	tick := func() { e.Tick(ctx); e.Wait() }
	tick() // what it first finds
	screens = 2
	tick()
	if len(got) != 1 || got[0] != "desk|display|a display was connected" {
		t.Fatalf("%q", got)
	}
	autos, _ := db.Automations(ctx)
	if len(autos) != 1 {
		t.Fatalf("%+v", autos)
	}
	runs, err := db.AutomationRuns(ctx, autos[0].ID, 10)
	if err != nil || len(runs) != 1 || runs[0].State != RunDone {
		t.Fatalf("%+v %v", runs, err)
	}
	// The file changes: the automation goes with the next sync.
	writeFile(t, file, "name = \"Desk 2\"\n[[trigger]]\nkind = \"display\"\nevent = \"connect\"\n")
	set.Load(ctx)
	sync.Now(ctx)
	screens = 1
	now = now.Add(2 * time.Minute)
	tick()
	screens = 2
	tick()
	if len(got) != 1 {
		t.Fatalf("ran for a changed file: %q", got)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
