package automations

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestAlertRulesAreAutomations(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n, err := MigrateAlertRules(ctx, db)
	if err != nil || n == 0 {
		t.Fatal("the default rules move", n, err)
	}
	changed := 0
	db.SetAlertRuleStore(AlertRules{DB: db, Changed: func() { changed++ }})
	rules, err := db.AlertRules(ctx)
	if err != nil || len(rules) != n {
		t.Fatalf("%d rules: %v", len(rules), err)
	}
	autos, _ := db.Automations(ctx)
	for _, a := range autos {
		s, err := spec.Parse(a.Spec)
		if err != nil {
			t.Fatalf("%s: every default rule passes the validator: %v", a.Name, err)
		}
		if a.Origin != OriginAlert || s.Trigger.Kind != "alert" || s.Then[0].Channels[0] != "all" || s.Then[0].Text != "{alert}" {
			t.Fatalf("%+v", s)
		}
	}

	rule := store.AlertRule{Name: "Hot", Metric: "cpu.temp", Op: ">", Threshold: 90, ForSec: 30, Severity: "critical", Enabled: true, QuietFrom: "23:00", QuietTo: "07:00"}
	id, err := db.CreateAlertRule(ctx, rule)
	if err != nil || changed != 1 {
		t.Fatal(id, err)
	}
	a, _ := db.GetAutomation(ctx, id)
	s, _ := spec.Parse(a.Spec)
	if s.Options.QuietFrom != "23:00" || s.Options.CooldownS != alertCooldown || s.Then[0].Severity != "critical" {
		t.Fatalf("%+v", s)
	}
	// A step added on the Automations page survives an edit on the Alerts page.
	s.Then = append(s.Then, spec.Step{Step: "action", Action: "power/profile", Target: "power-saver"})
	a.Spec, _ = json.Marshal(s)
	if err := db.UpdateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	rule.ID, rule.Threshold, rule.Severity, rule.Name = id, 85, "warning", "Warm"
	if err := db.UpdateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	a, _ = db.GetAutomation(ctx, id)
	s, _ = spec.Parse(a.Spec)
	if a.Name != "Warm" || s.Trigger.Threshold != 85 || s.Then[0].Severity != "warning" || len(s.Then) != 2 {
		t.Fatalf("%s %+v", a.Name, s)
	}
	// Bad rules are refused by the one validator.
	for _, bad := range []store.AlertRule{
		{Name: "", Metric: "cpu.temp", Op: ">", Threshold: 90, Severity: "info"},
		{Name: "x", Metric: "shell", Op: ">", Threshold: 1, Severity: "info"},
		{Name: "x", Metric: "cpu.temp", Op: ">", Threshold: 900, Severity: "info"},
	} {
		if _, err := db.CreateAlertRule(ctx, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	// Only alert automations are rules.
	ids, _ := db.CreateAutomations(ctx, []store.Automation{{Name: "other", Spec: json.RawMessage(`{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}],"options":{"cooldown_s":300,"max_per_hour":6}}`), Origin: "ui"}})
	if err := db.DeleteAlertRule(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted a non-alert automation as a rule", err)
	}
	if err := db.UpdateAlertRule(ctx, store.AlertRule{ID: ids[0], Name: "x", Metric: "cpu.temp", Op: ">", Threshold: 1, Severity: "info"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := db.DeleteAlertRule(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAutomation(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestFireAlertRunsTheRule(t *testing.T) {
	g := newEngineRig(t)
	s := &sent{}
	g.e.cfg.Steps = s.steps()
	raw, _ := json.Marshal(RuleSpec(store.AlertRule{Metric: "disk.used_pct", Op: ">", Threshold: 90, Severity: "warning"}))
	g.add("Disk almost full", string(raw))
	// The automations engine never evaluates an alert trigger itself.
	g.observe("disk.used_pct", map[string]float64{"/": 99})
	if len(s.list) != 0 {
		t.Fatalf("%q", s.list)
	}
	g.e.FireAlert(context.Background(), 1, "/", 95, "Disk space used (/) is 95%, above 90%.")
	g.e.FireAlert(context.Background(), 99, "/", 95, "no such rule")
	g.e.Wait()
	if len(s.list) != 1 || s.list[0] != "all:warning|Disk almost full|Disk space used (/) is 95%, above 90%." {
		t.Fatalf("%q", s.list)
	}
	if st := g.st.states(); len(st) != 1 || !strings.HasPrefix(st[0], "done") {
		t.Fatalf("%v", st)
	}
}

// Incidents are per instance: a rule whose two disks fill within a minute
// sends two notifications, as alerts always did, despite the cooldown.
func TestFireAlertEachInstance(t *testing.T) {
	g := newEngineRig(t)
	s := &sent{}
	g.e.cfg.Steps = s.steps()
	raw, _ := json.Marshal(RuleSpec(store.AlertRule{Metric: "disk.used_pct", Op: ">", Threshold: 90, Severity: "warning"}))
	g.add("Disk almost full", string(raw))
	g.e.FireAlert(context.Background(), 1, "/", 95, "/ is full")
	g.e.FireAlert(context.Background(), 1, "/home", 96, "/home is full\x1b[31m")
	g.e.Wait()
	if len(s.list) != 2 {
		t.Fatalf("%q", s.list)
	}
	for _, l := range s.list {
		if strings.ContainsRune(l, 0x1b) {
			t.Fatalf("control character kept: %q", l)
		}
	}
}
