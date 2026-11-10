package alerts

import (
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestTracker(t *testing.T) {
	var tr Tracker
	r := store.AlertRule{Metric: "cpu.temp", Op: ">", Threshold: 90, ForSec: 60}
	t0 := time.Unix(1000, 0)
	steps := []struct {
		at   int
		v    float64
		want Change
	}{
		{0, 95, Unchanged}, {30, 95, Unchanged}, {60, 95, Opened}, {90, 99, Unchanged},
		{100, 88, Unchanged}, // inside the 5° hysteresis
		{120, 84, Closed}, {130, 95, Unchanged}, {140, 80, Unchanged}, {150, 95, Unchanged},
	}
	for _, s := range steps {
		if got, _ := tr.Step(1, r, "", s.v, t0.Add(time.Duration(s.at)*time.Second)); got != s.want {
			t.Errorf("at %d v %g: %v, want %v", s.at, s.v, got, s.want)
		}
	}
	// Instances are apart; a gone instance is forgotten.
	ev := store.AlertRule{Metric: "unit.failed", Op: ">", Threshold: 0.5}
	if c, _ := tr.Step(2, ev, "a.service", 1, t0); c != Opened {
		t.Fatal(c)
	}
	if c, _ := tr.Step(2, ev, "b.service", 1, t0); c != Opened {
		t.Fatal(c)
	}
	tr.Gone(2, map[string]float64{"b.service": 1})
	if c, _ := tr.Step(2, ev, "a.service", 1, t0); c != Opened {
		t.Fatal("a forgotten instance did not open afresh")
	}
	if c, d := tr.Step(2, ev, "b.service", 0, t0.Add(time.Minute)); c != Closed || d != time.Minute {
		t.Fatal(c, d)
	}
	tr.Forget(2)
	if c, _ := tr.Step(2, ev, "a.service", 1, t0); c != Opened {
		t.Fatal("forget")
	}
}
