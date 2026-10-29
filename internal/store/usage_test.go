package store

import (
	"context"
	"testing"
	"time"
)

func TestItemUsageMergeTopAndPrune(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	h := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	rows := []ItemUsage{
		{Item: "unit:cups.service", Kind: "service", Name: "cups", Time: h, CPUAvg: 2, CPUMax: 4, MemAvg: 100, MemMax: 200, N: 30},
		{Item: "app:org.gnome.Nautilus", Kind: "app", Name: "Files", Time: h, CPUAvg: 10, CPUMax: 50, MemAvg: 300, MemMax: 400, N: 60},
		{Item: "unit:cups.service", Kind: "service", Name: "cups", Time: h.Add(time.Hour), CPUAvg: 1, CPUMax: 1, MemAvg: 100, MemMax: 100, N: 60},
	}
	if err := db.AddItemUsage(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// hearthd restarted within the first hour: the rest of it is merged.
	if err := db.AddItemUsage(ctx, []ItemUsage{{Item: "unit:cups.service", Kind: "service", Name: "CUPS", Time: h, CPUAvg: 8, CPUMax: 9, MemAvg: 400, MemMax: 500, N: 30}}); err != nil {
		t.Fatal(err)
	}
	hist, err := db.ItemHistory(ctx, "unit:cups.service", h, h.Add(2*time.Hour))
	if err != nil || len(hist) != 2 {
		t.Fatalf("history %v %+v", err, hist)
	}
	if r := hist[0]; r.CPUAvg != 5 || r.CPUMax != 9 || r.MemAvg != 250 || r.MemMax != 500 || r.N != 60 || r.Name != "CUPS" || !r.Time.Equal(h) {
		t.Fatalf("merged hour %+v", r)
	}

	top, err := db.TopItems(ctx, h, h.Add(2*time.Hour), 10)
	if err != nil || len(top) != 2 {
		t.Fatalf("top %v %+v", err, top)
	}
	if top[0].Item != "app:org.gnome.Nautilus" || top[0].Busy != 10*60/60.0/100 || top[0].Hours != 1 {
		t.Fatalf("top[0] %+v", top[0])
	}
	// cups: 5% for 60 samples + 1% for 60 = 3% average, 0.06 machine-hours.
	if c := top[1]; c.Name != "cups" || c.CPUAvg != 3 || c.CPUMax != 9 || c.Hours != 2 || c.Busy < 0.0599 || c.Busy > 0.0601 {
		t.Fatalf("top[1] %+v", c)
	}
	if top, _ := db.TopItems(ctx, h, h.Add(2*time.Hour), 1); len(top) != 1 {
		t.Fatalf("limit: %+v", top)
	}

	if err := db.PruneItemUsage(ctx, h.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if hist, _ := db.ItemHistory(ctx, "unit:cups.service", h, h.Add(2*time.Hour)); len(hist) != 1 || !hist[0].Time.Equal(h.Add(time.Hour)) {
		t.Fatalf("after prune %+v", hist)
	}
}

func TestAuditAndAlertsBetween(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	for i, a := range []string{"old", "in", "new"} {
		if err := db.Audit(ctx, AuditEntry{Time: t0.Add(time.Duration(i) * time.Hour), Origin: OriginLocal, Action: a, Target: "x", Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.AuditBetween(ctx, t0.Add(time.Hour), t0.Add(2*time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "in" {
		t.Fatalf("%v %+v", err, rows)
	}
	id, err := db.CreateAlertRule(ctx, AlertRule{Name: "CPU", Metric: "cpu.usage", Op: ">", Threshold: 90, Severity: "warning", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{t0, t0.Add(time.Hour)} {
		if _, err := db.OpenAlertEvent(ctx, AlertEvent{RuleID: id, RuleName: "CPU", Metric: "cpu.usage", Severity: "warning", Op: ">", Threshold: 90, Value: 95, Peak: 95, StartedAt: at.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := db.AlertEventsBetween(ctx, t0.Add(time.Hour), t0.Add(2*time.Hour), 10)
	if err != nil || len(evs) != 1 || evs[0].StartedAt != t0.Add(time.Hour).UnixMilli() {
		t.Fatalf("%v %+v", err, evs)
	}
}
