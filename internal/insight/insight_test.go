package insight

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func ptr(v float64) *float64 { return &v }

func sample(cpu float64, temp *float64) collect.Sample {
	s := collect.Sample{Time: now, CPUTempC: temp}
	s.CPU.Usage = cpu
	return s
}

func TestDiagnose(t *testing.T) {
	busy := []collect.ItemNow{{ID: "app:a", Name: "Blender", CPU: 62}, {ID: "unit:b", Name: "indexer", CPU: 12}, {ID: "unit:c", Name: "small", CPU: 1}}
	gpu := sample(10, ptr(70))
	gpu.GPUs = []collect.GPU{{Name: "Radeon", UtilPct: ptr(90)}, {Name: "Asleep", Sleeping: true}}
	old := sample(90, nil)
	old.Time = now.Add(-10 * time.Minute)
	moment := sample(90, nil)
	moment.Time = now.Add(-time.Minute)
	hotter := sample(10, ptr(90))
	hotter.Time = now.Add(-time.Minute)
	hotLongAgo := hotter
	hotLongAgo.Time = now.Add(-10 * time.Minute)
	first := moment
	first.Partial = true

	for name, c := range map[string]struct {
		s      collect.Sample
		recent []collect.Sample
		top    []collect.ItemNow
		want   []string
	}{
		"busy apps, busiest first":    {sample(80, ptr(90)), nil, busy, []string{CauseApp, CauseApp}},
		"gpu":                         {gpu, nil, nil, []string{CauseGPU}},
		"app and gpu":                 {gpu, nil, busy[:1], []string{CauseApp, CauseGPU}},
		"many small things":           {sample(60, ptr(70)), nil, busy[2:], []string{CauseSystem}},
		"load just ended":             {sample(5, ptr(80)), []collect.Sample{moment}, nil, []string{CauseCooling}},
		"load long ago, hot":          {sample(5, ptr(80)), []collect.Sample{old}, nil, []string{CauseHotIdle}},
		"quiet":                       {sample(5, ptr(45)), nil, nil, []string{CauseQuiet}},
		"a partial sample is no load": {sample(5, ptr(45)), []collect.Sample{first}, nil, []string{CauseQuiet}},
		"temperature falling":         {sample(5, ptr(60)), []collect.Sample{hotter}, nil, []string{CauseCooling}},
		"temperature fell long ago":   {sample(5, ptr(60)), []collect.Sample{hotLongAgo}, nil, []string{CauseQuiet}},
		"quiet with no sensor":        {sample(5, nil), nil, nil, []string{CauseQuiet}},
	} {
		d := Diagnose(c.s, c.recent, c.top)
		got := []string{}
		for _, x := range d.Causes {
			got = append(got, x.Code)
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %v", name, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v", name, got)
			}
		}
		if d.Fans == nil || d.Top == nil || d.GPUs == nil {
			t.Errorf("%s: a nil list would reach the UI as null", name)
		}
	}
	if d := Diagnose(sample(80, nil), nil, busy); d.Causes[0].Subject != "Blender" || d.Causes[0].Value != 62 {
		t.Errorf("%+v", d.Causes)
	}
	many := []collect.ItemNow{{CPU: 20}, {CPU: 20}, {CPU: 20}, {CPU: 20}}
	if d := Diagnose(sample(80, nil), nil, many); len(d.Causes) != maxCauses {
		t.Errorf("%+v", d.Causes)
	}
}

func TestFit(t *testing.T) {
	// 6 W idle, 20 W more at full CPU.
	var cpu, watts []float64
	for i := range minFitHours {
		c := float64(i) * 10
		cpu, watts = append(cpu, c), append(watts, 6+c/100*20)
	}
	if full, ok := fit(cpu, watts); !ok || math.Abs(full-20) > 1e-9 {
		t.Errorf("%v %v", full, ok)
	}
	if _, ok := fit(cpu[:minFitHours-1], watts[:minFitHours-1]); ok {
		t.Error("too few hours")
	}
	same := make([]float64, minFitHours)
	if _, ok := fit(same, watts); ok {
		t.Error("no spread in CPU")
	}
	falling := make([]float64, minFitHours)
	for i := range falling {
		falling[i] = 50 - float64(i)
	}
	if _, ok := fit(cpu, falling); ok {
		t.Error("power falling with CPU is not a fit")
	}
}

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func metric(t *testing.T, db *store.DB, name string, ts time.Time, v float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO metrics_1h (metric, ts, avg, min, max, n) VALUES (?, ?, ?, ?, ?, 1)`, name, ts.Unix(), v, v, v); err != nil {
		t.Fatal(err)
	}
}

func usage(t *testing.T, db *store.DB) {
	t.Helper()
	h := now.Add(-3 * time.Hour).Truncate(time.Hour)
	if err := db.AddItemUsage(context.Background(), []store.ItemUsage{
		{Item: "app:a", Kind: "app", Name: "Blender", Time: h, CPUAvg: 50, CPUMax: 80, N: 60},
		{Item: "unit:b", Kind: "service", Name: "cups", Time: h, CPUAvg: 10, CPUMax: 20, N: 60},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnergyNominal(t *testing.T) {
	db := open(t)
	usage(t, db)
	a := New(Config{Store: db, Clock: func() time.Time { return now }})
	e, err := a.Energy(context.Background(), "day")
	if err != nil {
		t.Fatal(err)
	}
	if e.Fitted || e.FullWatts != nominalWatts || len(e.Items) != 2 || e.Items[0].Name != "Blender" {
		t.Fatalf("%+v", e)
	}
	// Half the machine for an hour at 30 W is 15 Wh.
	if math.Abs(e.Items[0].Wh-15) > 1e-9 || math.Abs(e.Items[0].Share-500.0/6) > 1e-9 {
		t.Errorf("%+v", e.Items[0])
	}
	if _, err := a.Energy(context.Background(), "year"); err == nil {
		t.Error("unknown period")
	}
}

func TestEnergyLearnsFromBattery(t *testing.T) {
	db := open(t)
	usage(t, db)
	base := now.Add(-48 * time.Hour).Truncate(time.Hour)
	pct := 100.0
	for i := range minFitHours + 2 {
		ts := base.Add(time.Duration(i) * time.Hour)
		c := float64(i%5) * 20
		metric(t, db, "cpu.usage", ts, c)
		metric(t, db, "battery.pct:BAT1", ts, pct)
		metric(t, db, "battery.rate:BAT1", ts, 6+c/100*20)
		pct -= 3
	}
	// Charging afterwards, at a rate that would spoil the fit.
	for i := range 6 {
		ts := base.Add(time.Duration(minFitHours+2+i) * time.Hour)
		metric(t, db, "cpu.usage", ts, 1)
		metric(t, db, "battery.pct:BAT1", ts, pct)
		metric(t, db, "battery.rate:BAT1", ts, 60)
		pct += 3
	}
	a := New(Config{Store: db, Clock: func() time.Time { return now }})
	e, err := a.Energy(context.Background(), "week")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Fitted || math.Abs(e.FullWatts-20) > 1e-6 || e.FitHours < minFitHours {
		t.Fatalf("%+v", e)
	}
	if math.Abs(e.Items[0].Wh-10) > 1e-6 {
		t.Errorf("%+v", e.Items[0])
	}
}

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func TestHandlers(t *testing.T) {
	db := open(t)
	probes := 0
	var probeErr error
	stale := sample(0, ptr(94))
	stale.Time = now.Add(-time.Minute)
	a := New(Config{Store: db, Clock: func() time.Time { return now }, Samples: func() []collect.Sample { return []collect.Sample{stale} },
		Probe: func() (collect.Sample, error) {
			probes++
			return sample(float64(probes)*30, ptr(50)), probeErr
		},
		Busy: func(context.Context, time.Duration, int) ([]collect.ItemNow, error) {
			return []collect.ItemNow{{Name: "x", CPU: 50}}, nil
		}})
	m := mux{http.NewServeMux()}
	a.Register(m)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		return rec
	}
	rec := get("/api/v1/insight/fan")
	var d Diagnosis
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || d.Causes[0].Code != CauseApp {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	// The second reading, over the window: not the first, nor a stored one.
	if probes != 2 || d.CPU != 60 || d.TempC == nil || *d.TempC != 50 {
		t.Errorf("%d %+v", probes, d)
	}
	probeErr = errors.New("no /proc")
	if rec := get("/api/v1/insight/fan"); rec.Code != 500 {
		t.Errorf("probe failed: %d", rec.Code)
	}
	if rec := get("/api/v1/insight/energy?period=week"); rec.Code != 200 {
		t.Errorf("%d", rec.Code)
	}
	if rec := get("/api/v1/insight/energy?period=x"); rec.Code != 400 {
		t.Errorf("%d", rec.Code)
	}
}
