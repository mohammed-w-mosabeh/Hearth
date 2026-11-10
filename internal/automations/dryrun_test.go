package automations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func parsed(t *testing.T, raw string) spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// dryRig is an engine whose history is synthetic.
func dryRig(t *testing.T) *engineRig {
	g := newEngineRig(t)
	g.now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	from := g.now.Add(-DryRunDays * 24 * time.Hour)
	minutes := func(vals ...float64) []store.Agg {
		var out []store.Agg
		for i, v := range vals {
			out = append(out, store.Agg{Time: from.Add(time.Duration(i) * time.Minute), Avg: v, Min: v, Max: v, N: 60})
		}
		return out
	}
	g.e.cfg.History = History{
		Elapses: func(_ context.Context, expr string, f, to time.Time) ([]time.Time, bool, error) {
			if expr != "*:0/10" {
				return nil, false, errors.New("unexpected")
			}
			var out []time.Time
			for at := f.Truncate(10 * time.Minute).Add(10 * time.Minute); at.Before(to); at = at.Add(10 * time.Minute) {
				out = append(out, at)
			}
			return out, false, nil
		},
		Series: func(_ context.Context, metric string, _, _ time.Time) ([]store.Agg, error) {
			if metric != "cpu.temp" {
				return nil, errors.New("no series")
			}
			// Hot for 5 minutes, cool, then hot again after a gap.
			s := minutes(50, 50, 95, 95, 95, 95, 95, 50, 50)
			s = append(s, store.Agg{Time: from.Add(3 * time.Hour), Avg: 96, N: 60}, store.Agg{Time: from.Add(3*time.Hour + time.Minute), Avg: 96, N: 60},
				store.Agg{Time: from.Add(3*time.Hour + 2*time.Minute), Avg: 96, N: 60})
			return s, nil
		},
		SeriesLike: func(_ context.Context, prefix string, _, _ time.Time, _ int) (map[string][]store.Agg, error) {
			if prefix != "disk.used" {
				return nil, errors.New("no series")
			}
			return map[string][]store.Agg{"/": minutes(80, 92, 93), "/home": minutes(10, 10, 10), "/gone": minutes(99, 99)}, nil
		},
		Totals: func() Totals { return Totals{Disks: map[string]float64{"/": 100, "/home": 100}} },
	}
	return g
}

// A schedule every 10 minutes with at most 4 runs an hour: every elapse
// is listed in the total, 4 an hour would run, and nothing ran.
func TestDryRunSchedule(t *testing.T) {
	g := dryRig(t)
	s := parsed(t, `{"trigger":{"kind":"schedule","calendar":"*:0/10"},
		"then":[{"step":"action","action":"power/profile","target":"power-saver"},{"step":"action","action":"container/restart","target":"web"},
		{"step":"notify","channels":["desktop"],"text":"done"}],
		"options":{"cooldown_s":60,"max_per_hour":4}}`)
	out, err := g.e.DryRun(context.Background(), 0, s)
	if err != nil {
		t.Fatal(err)
	}
	if out.Trigger.State != CoverReplayed || out.Total != 7*24*6-1 || len(out.Firings) != DryRunListed {
		t.Fatalf("%+v total %d listed %d", out.Trigger, out.Total, len(out.Firings))
	}
	// The hourly maximum is a rolling hour, as the engine counts it.
	var ran []time.Time
	for _, f := range out.Firings {
		if f.Outcome != "run" {
			continue
		}
		n := 0
		for _, r := range ran {
			if !r.Before(f.Time.Add(-time.Hour)) {
				n++
			}
		}
		if n >= 4 {
			t.Fatalf("a fifth run within an hour at %v", f.Time)
		}
		ran = append(ran, f.Time)
	}
	if out.Runs == 0 || out.Runs >= out.Total {
		t.Fatalf("runs %d of %d", out.Runs, out.Total)
	}
	skipped := 0
	for _, f := range out.Firings {
		if f.Outcome == "skipped" && f.Reason == "the hourly maximum was reached" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("the hourly maximum never applied")
	}
	want := []string{DryUnattended, DryApproval, DryApproval}
	for i, st := range out.Steps {
		if st.Outcome != want[i] {
			t.Fatalf("step %d: %+v", i, out.Steps)
		}
	}
	if g.ranList() != "" || len(g.st.states()) != 0 || len(g.st.audit) != 0 {
		t.Fatalf("a dry run executed something: %q %v %v", g.ranList(), g.st.states(), g.st.audit)
	}
}

// Metric replays use minute averages through the alert evaluator: once
// per incident, after its duration; a gap starts afresh.
func TestDryRunMetric(t *testing.T) {
	g := dryRig(t)
	s := parsed(t, `{"trigger":{"kind":"metric","metric":"cpu.temp","op":">","threshold":90,"for_s":120},
		"then":[{"step":"action","action":"power/profile","target":"power-saver"}]}`)
	out, err := g.e.DryRun(context.Background(), 0, s)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 || out.Runs != 2 || !strings.Contains(out.Trigger.Note, "1-minute") {
		t.Fatalf("%+v", out)
	}
	if got := out.Firings[0].Time.Sub(g.now.Add(-DryRunDays * 24 * time.Hour)); got != 5*time.Minute {
		t.Fatalf("first firing after %v", got)
	}
	// A duration under a minute is rounded up and said.
	s.Trigger.ForS = 30
	out, _ = g.e.DryRun(context.Background(), 0, s)
	if !strings.Contains(out.Trigger.Note, "rounded up") {
		t.Fatalf("%+v", out.Trigger)
	}

	// Disk percent from used bytes and today's size, per mount; a mount
	// without a known size is left out.
	d := parsed(t, `{"trigger":{"kind":"metric","metric":"disk.used_pct","op":">","threshold":90},
		"then":[{"step":"action","action":"power/profile","target":"x"}]}`)
	out, err = g.e.DryRun(context.Background(), 0, d)
	if err != nil || out.Total != 1 || out.Firings[0].Instance != "/" || out.Trigger.State != CoverPartial {
		t.Fatalf("%+v %v", out, err)
	}
}

// Time and weekday conditions are evaluated when each firing happened;
// a condition nothing records leaves the firing "unknown", never false.
func TestDryRunConditions(t *testing.T) {
	g := dryRig(t)
	s := parsed(t, `{"trigger":{"kind":"schedule","calendar":"*:0/10"},
		"if":{"match":"all","conditions":[{"kind":"weekday","days":["mon"]},{"kind":"game","is":false}]},
		"then":[{"step":"action","action":"power/profile","target":"x"}],"options":{"cooldown_s":60,"max_per_hour":60}}`)
	out, err := g.e.DryRun(context.Background(), 0, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Conditions) != 2 || out.Conditions[0].State != CoverReplayed || out.Conditions[1].State != CoverNone {
		t.Fatalf("%+v", out.Conditions)
	}
	for _, f := range out.Firings {
		monday := f.Time.Weekday() == time.Monday
		if (monday && (f.Outcome != "unknown" || !strings.Contains(f.Reason, "game"))) || (!monday && f.Reason != "conditions") {
			t.Fatalf("%v %s %s", f.Time.Weekday(), f.Outcome, f.Reason)
		}
	}
	if out.Runs != 0 {
		t.Fatal("a run counted on an unknown condition")
	}
}

// Triggers nothing records say so, rather than "never".
func TestDryRunNoHistory(t *testing.T) {
	g := dryRig(t)
	for _, raw := range []string{
		`{"trigger":{"kind":"wifi","uuid":"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11","state":"up"},"then":[{"step":"wait","seconds":1}]}`,
		`{"trigger":{"kind":"metric","metric":"battery.discharging","op":"<","threshold":20},"then":[{"step":"wait","seconds":1}]}`,
		`{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}]}`,
	} {
		out, err := g.e.DryRun(context.Background(), 0, parsed(t, raw))
		if err != nil || out.Trigger.State != CoverNone || out.Trigger.Note == "" || out.Total != 0 {
			t.Fatalf("%s: %+v %v", raw, out, err)
		}
	}
}

// One dry run at a time.
func TestDryRunOneAtATime(t *testing.T) {
	g := dryRig(t)
	g.e.dry.Lock()
	defer g.e.dry.Unlock()
	if _, err := g.e.DryRun(context.Background(), 0, parsed(t, `{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}]}`)); !errors.Is(err, ErrDryRunBusy) {
		t.Fatal(err)
	}
}

// Test now runs a turned-off automation once, as if triggered, by "test":
// what may run unattended runs, the rest waits for approval; it ignores
// the cooldown, and is refused while paused.
func TestTestNow(t *testing.T) {
	g := newEngineRig(t)
	id := g.add("t", `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"power/profile","target":"a"},
		{"step":"action","action":"container/restart","target":"web"}],"options":{"cooldown_s":86400,"max_per_hour":1}}`)
	g.st.autos[0].Enabled = false
	ctx := context.Background()
	for range 2 {
		if err := g.e.Test(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		g.e.Wait()
	}
	if g.ranList() != "power/profile a,power/profile a" {
		t.Fatalf("%q", g.ranList())
	}
	if len(g.st.approvs) != 2 {
		t.Fatalf("%d approvals", len(g.st.approvs))
	}
	if strings.Contains(strings.Join(g.notes, ","), "Waiting") {
		t.Fatal("a test notified about its own approval")
	}
	// Its approval stays valid although the automation is off.
	ap := g.st.approvs[0]
	ap.AutomationID = id
	p, err := decodeParked(ap)
	if err != nil || !p.Test {
		t.Fatal(p, err)
	}
	if why, _ := g.e.stale(ctx, ap, p); why != "" {
		t.Fatalf("stale: %s", why)
	}
	if err := g.e.SetPaused(ctx, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := g.e.Test(ctx, id, nil); !errors.Is(err, ErrNotTestable) {
		t.Fatal(err)
	}
	if err := g.e.Test(ctx, 99, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

// Test now on an automation turned off, end to end: the unattended step
// runs, the approval screen runs the rest attended, and the run ends done
// by "test". A test of one that was on expires when it is turned off.
func TestTestNowApprovedEndToEnd(t *testing.T) {
	g, _ := approvalRig(t)
	id := g.add("t", `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"power/profile","target":"a"},
		{"step":"action","action":"container/restart","target":"web"}]}`)
	g.st.autos[0].Enabled = false
	if err := g.e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.e.Test(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	g.e.Wait()
	if res, err := approve(t, g, "automation/approve", "1"); err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if g.ranList() != "power/profile a,container/restart web (attended)" {
		t.Fatalf("%q", g.ranList())
	}
	state, rec := runRecord(t, g, 0)
	if state != RunDone || rec.By != "test" {
		t.Fatalf("%s %+v", state, rec)
	}

	h, _ := approvalRig(t)
	h.add("on", `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"container/restart","target":"web"}]}`)
	if err := h.e.Test(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
	h.e.Wait()
	h.st.autos[0].Enabled = false
	if _, err := approve(t, h, "automation/approve", "1"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("a test of an automation that was on outlived turning it off: %v", err)
	}
}

// A step that waits in one branch does not make the other branch wait,
// but whatever follows the check waits.
func TestDryStepsBranches(t *testing.T) {
	g := dryRig(t)
	s := parsed(t, `{"trigger":{"kind":"login"},"then":[
		{"step":"check","what":"unit_state","target":"user/x.service","expect":"active",
		 "then":[{"step":"action","action":"container/restart","target":"web"}],
		 "else":[{"step":"action","action":"power/profile","target":"a"}]},
		{"step":"notify","channels":["desktop"],"text":"done"}]}`)
	out, err := g.e.DryRun(context.Background(), 0, s)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, st := range out.Steps {
		got = append(got, st.Outcome)
	}
	if strings.Join(got, ",") != "unattended,approval,unattended,approval" {
		t.Fatalf("%v", got)
	}
}

// A metric with nothing recorded is "none", never "replayed, no firings".
func TestDryRunEmptyMetric(t *testing.T) {
	g := dryRig(t)
	g.e.cfg.History.Series = func(context.Context, string, time.Time, time.Time) ([]store.Agg, error) { return nil, nil }
	out, err := g.e.DryRun(context.Background(), 0, parsed(t, `{"trigger":{"kind":"metric","metric":"cpu.usage","op":">","threshold":90},"then":[{"step":"wait","seconds":1}]}`))
	if err != nil || out.Trigger.State != CoverNone {
		t.Fatalf("%+v %v", out.Trigger, err)
	}
}
