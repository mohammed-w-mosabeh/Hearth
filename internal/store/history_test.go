package store

import (
	"context"
	"testing"
	"time"
)

func TestMatchMetric(t *testing.T) {
	sel := []string{"cpu.usage", "net.rx:", "cpu.core."}
	for m, want := range map[string]bool{
		"cpu.usage": true, "cpu.usage2": false, "net.rx:wlp3s0": true, "net.tx:wlp3s0": false,
		"cpu.core.3": true, "cpu.freq": false, "net.rx": false,
	} {
		if MatchMetric(m, sel) != want {
			t.Errorf("%s: want %v", m, want)
		}
	}
}

func TestHistoryBuckets(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ins := func(metric string, at time.Time, avg, mx float64, n int) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO metrics_1m (metric, ts, avg, min, max, n) VALUES (?, ?, ?, ?, ?, ?)`,
			metric, at.Unix(), avg, avg, mx, n); err != nil {
			t.Fatal(err)
		}
	}
	// Two minutes in the first 10-minute bucket, weighted 1:3.
	ins("cpu.usage", t0, 10, 50, 1)
	ins("cpu.usage", t0.Add(time.Minute), 30, 40, 3)
	ins("net.rx:wlp3s0", t0.Add(10*time.Minute), 100, 200, 60)
	ins("net.rx:eth0", t0.Add(10*time.Minute), 5, 5, 60)
	ins("mem.used", t0, 1, 1, 60)                    // not selected
	ins("cpu.usage", t0.Add(-time.Hour), 99, 99, 60) // before from

	h, err := db.History(ctx, Res1m, []string{"cpu.usage", "net.rx:"}, t0, t0.Add(time.Hour), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.T) != 2 || h.T[0] != t0.UnixMilli() || h.T[1] != t0.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("t %v", h.T)
	}
	cpu := h.Avg["cpu.usage"]
	if len(cpu) != 2 || cpu[0] == nil || *cpu[0] != 25 || cpu[1] != nil || *h.Max["cpu.usage"][0] != 50 {
		t.Fatalf("cpu %v", cpu)
	}
	if w := h.Avg["net.rx:wlp3s0"]; w[0] != nil || *w[1] != 100 || *h.Max["net.rx:wlp3s0"][1] != 200 {
		t.Fatalf("wlp3s0 %v", w)
	}
	if _, ok := h.Avg["mem.used"]; ok || len(h.Avg) != 3 {
		t.Fatalf("selection %v", h.Avg)
	}
	if _, err := db.History(ctx, "5m", []string{"cpu.usage"}, t0, t0, time.Minute); err == nil {
		t.Fatal("unknown resolution accepted")
	}
}
