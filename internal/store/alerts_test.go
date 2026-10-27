package store

import (
	"context"
	"errors"
	"testing"
)

func TestAlertRules(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	rules, err := db.AlertRules(ctx)
	if err != nil || len(rules) != 8 || rules[7].Metric != "container.exited" || rules[0].Metric != "disk.used_pct" || !rules[0].Enabled || rules[1].ForSec != 300 {
		t.Fatalf("defaults %+v %v", rules, err)
	}
	id, err := db.CreateAlertRule(ctx, AlertRule{Name: "Home full", Metric: "disk.used_pct", Target: "/home", Op: ">", Threshold: 80,
		ForSec: 0, Severity: "info", Enabled: true, QuietFrom: "22:00", QuietTo: "07:00"})
	if err != nil {
		t.Fatal(err)
	}
	r := AlertRule{ID: id, Name: "Home nearly full", Metric: "disk.used_pct", Target: "/home", Op: ">", Threshold: 85, Severity: "warning"}
	if err := db.UpdateAlertRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	rules, _ = db.AlertRules(ctx)
	if got := rules[len(rules)-1]; got != r {
		t.Fatalf("updated %+v, want %+v", got, r)
	}
	if err := db.DeleteAlertRule(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAlertRule(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := db.UpdateAlertRule(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update deleted: %v", err)
	}
	// A bad op or severity is refused by the schema.
	if _, err := db.CreateAlertRule(ctx, AlertRule{Name: "x", Metric: "cpu.usage", Op: "=", Severity: "info"}); err == nil {
		t.Fatal("op = accepted")
	}
}

func TestAlertEvents(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	e := AlertEvent{RuleID: 1, RuleName: "Disk almost full", Metric: "disk.used_pct", Instance: "/", Severity: "warning",
		Op: ">", Threshold: 90, Value: 91, Peak: 91, StartedAt: 1000, Notified: true}
	id1, _ := db.OpenAlertEvent(ctx, e)
	e.Instance = "/home"
	id2, _ := db.OpenAlertEvent(ctx, e)
	if err := db.CloseAlertEvent(ctx, id1, 5000, 97); err != nil {
		t.Fatal(err)
	}
	if err := db.CloseAlertEvent(ctx, id1, 6000, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed twice: %v", err)
	}
	open, _ := db.OpenAlertEvents(ctx)
	if len(open) != 1 || open[0].ID != id2 || open[0].EndedAt != nil {
		t.Fatalf("open %+v", open)
	}
	all, _ := db.AlertEvents(ctx, 10, 0)
	if len(all) != 2 || all[0].ID != id2 || all[1].Peak != 97 || *all[1].EndedAt != 5000 || !all[1].Notified {
		t.Fatalf("all %+v", all)
	}
	if older, _ := db.AlertEvents(ctx, 10, id2); len(older) != 1 || older[0].ID != id1 {
		t.Fatalf("paged %+v", older)
	}
}
