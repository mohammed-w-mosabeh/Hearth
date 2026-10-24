package store

import (
	"context"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func newDS(t *testing.T, db *DB, c *clock) *Downsampler {
	t.Helper()
	d, err := NewDownsampler(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), c.now)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func series(t *testing.T, db *DB, res Resolution, metric string) []Agg {
	t.Helper()
	got, err := db.Series(context.Background(), res, metric, t0.Add(-400*24*time.Hour), t0.Add(400*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMinuteFlushOnRollover(t *testing.T) {
	db, _ := openTemp(t)
	c := &clock{t0}
	d := newDS(t, db, c)
	ctx := context.Background()

	for i, v := range []float64{10, 20, 30} {
		c.t = t0.Add(time.Duration(i*15) * time.Second)
		d.Add(ctx, c.t, []Point{{"cpu.usage", v}, {"mem.used", 100}})
	}
	if got := series(t, db, Res1m, "cpu.usage"); len(got) != 0 {
		t.Fatalf("flushed before the minute ended: %v", got)
	}

	c.t = t0.Add(time.Minute)
	d.Add(ctx, c.t, []Point{{"cpu.usage", 50}})
	got := series(t, db, Res1m, "cpu.usage")
	if len(got) != 1 || !got[0].Time.Equal(t0) || !approx(got[0].Avg, 20) || got[0].Min != 10 || got[0].Max != 30 || got[0].N != 3 {
		t.Fatalf("minute row: %+v", got)
	}
	if got := series(t, db, Res1m, "mem.used"); len(got) != 1 || got[0].Avg != 100 {
		t.Fatalf("second metric: %+v", got)
	}

	// Close flushes the partial minute.
	d.Close(ctx)
	if got := series(t, db, Res1m, "cpu.usage"); len(got) != 2 || got[1].Avg != 50 {
		t.Fatalf("after close: %+v", got)
	}
}

func TestMinuteMergeAfterRestart(t *testing.T) {
	db, _ := openTemp(t)
	c := &clock{t0}
	ctx := context.Background()

	d := newDS(t, db, c)
	d.Add(ctx, t0, []Point{{"x", 10}})
	d.Add(ctx, t0.Add(10*time.Second), []Point{{"x", 20}})
	d.Close(ctx) // restart within the same minute

	d = newDS(t, db, c)
	d.Add(ctx, t0.Add(30*time.Second), []Point{{"x", 60}})
	d.Close(ctx)

	got := series(t, db, Res1m, "x")
	if len(got) != 1 || got[0].N != 3 || !approx(got[0].Avg, 30) || got[0].Min != 10 || got[0].Max != 60 {
		t.Fatalf("merged row: %+v", got)
	}
}

func TestHourlyRollupWeighted(t *testing.T) {
	db, _ := openTemp(t)
	c := &clock{t0}
	d := newDS(t, db, c)
	ctx := context.Background()

	// Minute 0: 1 sample of 100 (slow sampling). Minute 1: 3 samples of 0
	// (fast sampling). A plain average of averages would say 50; weighted is 25.
	d.Add(ctx, t0, []Point{{"cpu.usage", 100}})
	for i := range 3 {
		c.t = t0.Add(time.Minute + time.Duration(i)*time.Second)
		d.Add(ctx, c.t, []Point{{"cpu.usage", 0}})
	}
	c.t = t0.Add(time.Hour)
	d.Add(ctx, c.t, []Point{{"cpu.usage", 7}}) // crosses the hour: flush + rollup

	got := series(t, db, Res1h, "cpu.usage")
	if len(got) != 1 || !got[0].Time.Equal(t0) || !approx(got[0].Avg, 25) || got[0].Min != 0 || got[0].Max != 100 || got[0].N != 4 {
		t.Fatalf("hour row: %+v", got)
	}
}

func TestRetention(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ins := func(table string, ts time.Time) {
		if _, err := db.ExecContext(ctx, `INSERT INTO `+table+` VALUES ('x', ?, 1, 1, 1, 1)`, ts.Unix()); err != nil { // #nosec G202 -- test constant
			t.Fatal(err)
		}
	}
	ins("metrics_1m", t0.Add(-Retention1m-time.Minute)) // expired
	ins("metrics_1m", t0.Add(-Retention1m+time.Hour))   // kept
	ins("metrics_1h", t0.Add(-Retention1h-time.Hour))   // expired
	ins("metrics_1h", t0.Add(-30*24*time.Hour))         // kept

	newDS(t, db, &clock{t0}) // catch-up prunes

	if got := series(t, db, Res1m, "x"); len(got) != 1 {
		t.Fatalf("1m after retention: %+v", got)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics_1h WHERE ts < ?`, t0.Add(-Retention1h).Unix()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired hourly rows remain: %d %v", n, err)
	}
}

func TestCatchUpAfterDowntime(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	c := &clock{t0}
	d := newDS(t, db, c)
	// One sample per hour for three hours, then hearthd stops.
	for h := range 3 {
		d.Add(ctx, t0.Add(time.Duration(h)*time.Hour), []Point{{"x", float64(h + 1)}})
		d.flush(ctx)
	}
	// Restart five hours later: every complete hour is rolled up.
	c.t = t0.Add(8 * time.Hour)
	newDS(t, db, c)
	got := series(t, db, Res1h, "x")
	if len(got) != 3 || got[0].Avg != 1 || got[2].Avg != 3 || !got[2].Time.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("hourly after catch-up: %+v", got)
	}
}

func TestDownsamplerSkipsNonFinite(t *testing.T) {
	db, _ := openTemp(t)
	c := &clock{t0}
	d := newDS(t, db, c)
	ctx := context.Background()
	d.Add(ctx, t0, []Point{{"x", math.NaN()}, {"x", math.Inf(1)}, {"x", 4}})
	d.Close(ctx)
	if got := series(t, db, Res1m, "x"); len(got) != 1 || got[0].N != 1 || got[0].Avg != 4 {
		t.Fatalf("%+v", got)
	}
}

func TestSeriesRejectsUnknownResolution(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Series(context.Background(), "1d; DROP TABLE audit_log", "x", t0, t0); err == nil {
		t.Fatal("accepted unknown resolution")
	}
}
