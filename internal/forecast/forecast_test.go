package forecast

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	gib = float64(1 << 30)
	tb  = 1000 * gib
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// hourly builds days of hourly points ending at now from v(day).
func hourly(days int, v func(d float64) float64) []Point {
	var pts []Point
	for h := days*24 - 1; h >= 0; h-- {
		at := now.Add(-time.Duration(h) * time.Hour)
		pts = append(pts, Point{At: at, V: v(float64(days) - float64(h)/24)})
	}
	return pts
}

func TestFlat(t *testing.T) {
	pts := hourly(30, func(float64) float64 { return 400 * gib })
	if f, ok := Disk(pts, tb, 600*gib, now); ok {
		t.Fatalf("forecast from a flat disk: %+v", f)
	}
}

func TestBarelyRising(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*0.01*gib })
	if f, ok := Disk(pts, tb, 600*gib, now); ok {
		t.Fatalf("0.3 GiB in a month on a terabyte is flat: %+v", f)
	}
}

func TestRising(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*5*gib })
	f, ok := Disk(pts, tb, 100*gib, now)
	if !ok {
		t.Fatal("no forecast")
	}
	if math.Abs(f.Days-20) > 0.01 || f.WindowDays != 7 || f.R2 < 0.99 {
		t.Fatalf("%+v", f)
	}
}

func TestTooLittleHistory(t *testing.T) {
	pts := hourly(2, func(d float64) float64 { return 400*gib + d*5*gib })
	if f, ok := Disk(pts, tb, 100*gib, now); ok {
		t.Fatalf("two days of data: %+v", f)
	}
}

func TestLongWindowCarriesASlowDisk(t *testing.T) {
	// A quiet last week over a month of steady growth: the week is flat,
	// the month is not.
	pts := hourly(30, func(d float64) float64 { return 400*gib + math.Min(d, 23)*2*gib })
	f, ok := Disk(pts, tb, 100*gib, now)
	if !ok || f.WindowDays != 30 {
		t.Fatalf("%+v %v", f, ok)
	}
}

func TestNoisy(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) // #nosec G404 -- a fixed seed keeps the test repeatable
	pts := hourly(30, func(float64) float64 { return 400*gib + r.Float64()*gib*4 })
	if f, ok := Disk(pts, tb, 100*gib, now); ok {
		t.Fatalf("forecast from noise: %+v", f)
	}
}

func TestNoisyButRising(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) // #nosec G404 -- a fixed seed keeps the test repeatable
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*5*gib + r.Float64()*gib })
	f, ok := Disk(pts, tb, 100*gib, now)
	if !ok || math.Abs(f.Days-20) > 2 {
		t.Fatalf("%+v %v", f, ok)
	}
}

func TestFreedSpace(t *testing.T) {
	// Rising, then 100 GiB deleted two days ago: too little since then,
	// and the climb before it no longer counts.
	pts := hourly(30, func(d float64) float64 {
		if d > 28 {
			return 300*gib + (d-28)*5*gib
		}
		return 300*gib + d*5*gib
	})
	if f, ok := Disk(pts, tb, 500*gib, now); ok {
		t.Fatalf("forecast across freed space: %+v", f)
	}
}

func TestRisingAfterFreedSpace(t *testing.T) {
	pts := hourly(30, func(d float64) float64 {
		if d > 24 {
			return 300*gib + (d-24)*5*gib
		}
		return 300*gib + d*7*gib
	})
	f, ok := Disk(pts, tb, 150*gib, now)
	if !ok || math.Abs(f.Days-30) > 0.1 {
		t.Fatalf("the fit must start after the drop: %+v %v", f, ok)
	}
}

func TestOneBigDownload(t *testing.T) {
	pts := hourly(30, func(d float64) float64 {
		if d > 26.5 {
			return 450 * gib
		}
		return 400 * gib
	})
	if f, ok := Disk(pts, tb, 100*gib, now); ok {
		t.Fatalf("one step is not a trend: %+v", f)
	}
}

func TestStaleHistory(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*5*gib })
	if f, ok := Disk(pts, tb, 100*gib, now.Add(3*day)); ok {
		t.Fatalf("forecast from old data: %+v", f)
	}
}

func TestBeyondTheHorizon(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*0.5*gib })
	if f, ok := Disk(pts, tb, 500*gib, now); ok {
		t.Fatalf("%+v", f)
	}
}

func TestSleepingLaptopGaps(t *testing.T) {
	var pts []Point
	for _, p := range hourly(14, func(d float64) float64 { return 400*gib + d*5*gib }) {
		if h := p.At.Hour(); h >= 8 && h < 20 {
			pts = append(pts, p)
		}
	}
	if f, ok := Disk(pts, tb, 100*gib, now); !ok || math.Abs(f.Days-20) > 0.5 {
		t.Fatalf("%+v %v", f, ok)
	}
}

func daily(days int, v func(d float64) float64) []Point {
	var pts []Point
	for d := days - 1; d >= 0; d-- {
		pts = append(pts, Point{At: now.Add(-time.Duration(d) * day), V: v(float64(days - d))})
	}
	return pts
}

func TestBatteryWear(t *testing.T) {
	w := Battery(daily(120, func(d float64) float64 { return 90 - d*0.01 }))
	if w.LossPerYear == nil || math.Abs(*w.LossPerYear-3.65) > 0.01 || len(w.T) != 120 || len(w.Pct) != 120 {
		t.Fatalf("%+v", w)
	}
	short := Battery(daily(20, func(d float64) float64 { return 90 - d*0.01 }))
	if short.LossPerYear != nil || len(short.T) != 20 {
		t.Fatalf("20 days give a chart, not a rate: %+v", short)
	}
	flat := Battery(daily(120, func(float64) float64 { return 90 }))
	if flat.LossPerYear != nil {
		t.Fatalf("%+v", flat)
	}
	rising := Battery(daily(120, func(d float64) float64 { return 80 + d*0.01 })) // recalibrated
	if rising.LossPerYear != nil {
		t.Fatalf("%+v", rising)
	}
	r := rand.New(rand.NewPCG(5, 6)) // #nosec G404 -- a fixed seed keeps the test repeatable
	noisy := Battery(daily(120, func(float64) float64 { return 85 + r.Float64()*6 }))
	if noisy.LossPerYear != nil {
		t.Fatalf("%+v", noisy)
	}
	if e := Battery(nil); e.LossPerYear != nil || len(e.T) != 0 || e.T == nil {
		t.Fatalf("%+v", e)
	}
}

// fakeHistory answers with used bytes and battery health.
type fakeHistory struct {
	used []Point
}

func (f *fakeHistory) History(_ context.Context, res store.Resolution, sel []string, _, _ time.Time, _ time.Duration) (store.History, error) {
	h := store.History{Avg: map[string][]*float64{}}
	if res != store.Res1h {
		return h, nil
	}
	if sel[0] == usedPrefix {
		col := make([]*float64, len(f.used))
		for i := range f.used {
			h.T = append(h.T, f.used[i].At.UnixMilli())
			col[i] = &f.used[i].V
		}
		h.Avg[usedPrefix+"/"] = col
		return h, nil
	}
	v := 88.0
	h.T = []int64{now.UnixMilli()}
	h.Avg[healthPrefix+"BAT0"] = []*float64{&v}
	return h, nil
}

func monitor(h History, mounts []Mount, seen *[]float64) *Monitor {
	return New(Config{DB: h, Now: func() time.Time { return now },
		Mounts: func() []Mount { return mounts },
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) {
			*seen = append(*seen, v["/"])
		}})
}

func TestAlertHysteresis(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*5*gib })
	var seen []float64
	mounts := []Mount{{Mount: "/", Total: uint64(tb), Avail: uint64(30 * gib)}} // 6 days
	m := monitor(&fakeHistory{used: pts}, mounts, &seen)
	ctx := context.Background()
	m.refresh(ctx)                     // fires
	mounts[0].Avail = uint64(40 * gib) // 8 days: held
	m.refresh(ctx)
	mounts[0].Avail = uint64(45 * gib) // 9 days: held
	m.refresh(ctx)
	mounts[0].Avail = uint64(60 * gib) // 12 days: clears
	m.refresh(ctx)
	mounts[0].Avail = uint64(40 * gib) // 8 days, not firing: stays quiet
	m.refresh(ctx)
	want := []float64{1, 1, 1, 0, 0}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("%v, want %v", seen, want)
		}
	}
}

func TestNoSampleNoAlert(t *testing.T) {
	var seen []float64
	m := monitor(&fakeHistory{}, nil, &seen)
	m.refresh(context.Background())
	if len(seen) != 0 {
		t.Fatalf("%v", seen)
	}
}

func TestHandle(t *testing.T) {
	pts := hourly(30, func(d float64) float64 { return 400*gib + d*5*gib })
	var seen []float64
	m := monitor(&fakeHistory{used: pts}, []Mount{{Mount: "/", Total: uint64(tb), Avail: uint64(100 * gib)}}, &seen)
	w := httptest.NewRecorder()
	m.Handle(w, httptest.NewRequest("GET", "/api/v1/forecasts", nil))
	var r Report
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Disks) != 1 || r.Disks[0].Mount != "/" || math.Abs(r.Disks[0].Days-20) > 0.1 {
		t.Fatalf("%s", w.Body.String())
	}
	if b, ok := r.Batteries["BAT0"]; !ok || len(b.Pct) != 1 {
		t.Fatalf("%s", w.Body.String())
	}
}

// Whatever the history holds, a forecast is a finite, positive number.
func FuzzDisk(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 1e12, 1e11)
	f.Fuzz(func(t *testing.T, raw []byte, capacity, avail float64) {
		var pts []Point
		at := now.Add(-20 * day)
		for len(raw) >= 8 {
			pts = append(pts, Point{At: at, V: math.Float64frombits(binary.LittleEndian.Uint64(raw))})
			raw = raw[8:]
			at = at.Add(time.Hour)
		}
		if d, ok := Disk(pts, capacity, avail, now); ok &&
			(math.IsNaN(d.Days) || math.IsInf(d.Days, 0) || d.Days < 0 || d.Days > horizonDays || d.PerDay <= 0) {
			t.Fatalf("%+v", d)
		}
		w := Battery(pts)
		if w.LossPerYear != nil && (math.IsNaN(*w.LossPerYear) || *w.LossPerYear <= 0 || *w.LossPerYear > maxLossPerYear) {
			t.Fatalf("%+v", w)
		}
	})
}
