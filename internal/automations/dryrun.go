package automations

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// "What would have happened?" (ROADMAP 12.8, docs/plan/automations.md §6):
// a spec's trigger and conditions are replayed against the last 7 days of
// stored history, through the engine's own quiet hours, cooldown and
// hourly maximum, and each step is previewed as an unattended run would
// preview it. Nothing runs: no action, notification, run record or audit.

// Dry run bounds.
const (
	DryRunDays    = 7
	DryRunListed  = 200 // firings listed; Total counts them all
	dryRunTimeout = 20 * time.Second
	dryRunBuckets = 7 * 24 * 60 * 8 // minute buckets read for one metric, all instances
	dryRunEvents  = 5000
)

// Coverage states of a trigger or condition in a dry run.
const (
	CoverReplayed = "replayed"
	CoverPartial  = "partial"
	CoverNone     = "none" // no history kept: never read as "it never fired"
)

// Coverage says how much of a trigger or condition the history covers.
type Coverage struct {
	State string `json:"state"`
	// Notes are codes the UI words: see the Note* constants.
	Notes []string `json:"notes"`
	// Since is when a metric's history starts, when it starts late.
	Since *time.Time `json:"since,omitempty"`
	// ForS is a duration under a minute that was rounded up to one.
	ForS int `json:"for_s,omitempty"`
}

// Coverage notes, as codes.
const (
	NoteExact        = "exact"         // a schedule, computed exactly
	NoteBoots        = "boots"         // each boot counts as a login
	NoteOutages      = "outages"       // from the recorded outages
	NoteAlertOwn     = "alert-own"     // this rule's own incidents
	NoteAlertWatched = "alert-watched" // only while an alert rule watched it
	NoteMinute       = "minute"        // from 1-minute averages
	NoteRounded      = "rounded"       // a duration rounded up to 60 s (ForS)
	NoteDiskSize     = "disk-size"     // against each disk's size today
	NoteSince        = "since"         // recorded only since Since
	NoteNoData       = "no-data"       // nothing recorded in these days
	NoteUnreadable   = "unreadable"    // the source could not be read
	NoteNotRecorded  = "not-recorded"  // nothing records it
	NoteNotYet       = "not-yet"       // kept, but not replayed yet
)

func cover(state string, notes ...string) Coverage { return Coverage{State: state, Notes: notes} }

// DryCondition is one condition's coverage.
type DryCondition struct {
	Kind string `json:"kind"`
	Coverage
}

// DryStep is how a step would go.
type DryStep struct {
	Step   string `json:"step"`
	Action string `json:"action,omitempty"`
	Target string `json:"target,omitempty"`
	Label  string `json:"label,omitempty"`
	// Outcome: unattended, approval (would wait for approval), refused or
	// fail. Branch is then or else for a check's step; Reason a code
	// (earlier-waits, no-actions); Detail the server's own words of a
	// refusal or failure.
	Outcome string `json:"outcome"`
	Branch  string `json:"branch,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Dry run step outcomes.
const (
	DryUnattended = "unattended"
	DryApproval   = "approval"
	DryRefused    = "refused"
	DryFail       = "fail"
)

// DryFiring is one time the trigger would have fired.
type DryFiring struct {
	Time     time.Time `json:"time"`
	Instance string    `json:"instance,omitempty"`
	Value    float64   `json:"value,omitempty"`
	// Outcome: run, skipped (Reason: quiet, cooldown, hourly, conditions)
	// or unknown (Missing lists the conditions nothing recorded).
	Outcome string   `json:"outcome"`
	Reason  string   `json:"reason,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// DryRun is the result.
type DryRun struct {
	From       time.Time      `json:"from"`
	To         time.Time      `json:"to"`
	Trigger    Coverage       `json:"trigger"`
	Conditions []DryCondition `json:"conditions"`
	Firings    []DryFiring    `json:"firings"`
	// Total counts every firing; Runs those that would have run.
	Total     int       `json:"total"`
	Runs      int       `json:"runs"`
	Truncated bool      `json:"truncated,omitempty"` // a source hit its bound
	Steps     []DryStep `json:"steps"`
}

// Totals are the sizes the stored history leaves out, from the live
// sample: memory and swap in bytes, each mounted filesystem's size.
type Totals struct {
	Mem, Swap float64
	Disks     map[string]float64
}

// History is what the dry run reads. Each may be nil: its triggers then
// have no history.
type History struct {
	// Elapses lists a calendar expression's elapses in (from, to).
	Elapses func(ctx context.Context, expr string, from, to time.Time) ([]time.Time, bool, error)
	// Series is one metric's minute buckets; SeriesLike every
	// "prefix:instance" metric's, by instance.
	Series     func(ctx context.Context, metric string, from, to time.Time) ([]store.Agg, error)
	SeriesLike func(ctx context.Context, prefix string, from, to time.Time, limit int) (map[string][]store.Agg, error)
	Totals     func() Totals
	// Events are the alert incidents that started in [from, to).
	Events  func(ctx context.Context, from, to time.Time, limit int) ([]store.AlertEvent, error)
	Outages func(ctx context.Context, limit int) ([]store.Outage, error)
	// Boots and Resumes are when the machine started and woke.
	Boots   func(ctx context.Context) ([]time.Time, error)
	Resumes func(ctx context.Context) ([]time.Time, error)
}

// noHistory are the triggers the dry run does not replay, with why.
var noHistory = map[string]string{
	"wifi": NoteNotRecorded, "vpn": NoteNotRecorded, "idle": NoteNotRecorded, "display": NoteNotRecorded,
	"files": NoteNotRecorded, "security_updates": NoteNotRecorded, "power": NoteNotRecorded,
	"ssh_failed": NoteNotYet, "app": NoteNotYet, "image_update": NoteNotYet,
}

// eventMetric are the event triggers replayed from alert incidents.
var eventMetric = map[string]bool{"container": true, "unit_failed": true, "new_listener": true, "usb_device": true, "integrity_changed": true}

// firing is one replayed trigger event.
type firing struct {
	at       time.Time
	instance string
	value    float64
}

// ErrDryRunBusy is returned while another dry run goes on.
var ErrDryRunBusy = errors.New("a dry run is already going; try again in a moment")

// DryRun replays s (of automation id, 0 when not saved yet) over the last
// DryRunDays days. Nothing runs.
func (e *Engine) DryRun(ctx context.Context, id int64, s spec.Spec) (DryRun, error) {
	if !e.dry.TryLock() {
		return DryRun{}, ErrDryRunBusy
	}
	defer e.dry.Unlock()
	ctx, cancel := context.WithTimeout(ctx, dryRunTimeout)
	defer cancel()
	to := e.cfg.Clock()
	from := to.Add(-DryRunDays * 24 * time.Hour)
	out := DryRun{From: from, To: to, Conditions: []DryCondition{}, Firings: []DryFiring{}, Steps: e.drySteps(ctx, s)}
	fires, cov, trunc, err := e.replay(ctx, id, s.Trigger, from, to)
	if err != nil {
		return DryRun{}, err
	}
	out.Trigger, out.Truncated = cov, trunc
	if s.If != nil {
		for _, c := range s.If.Conditions {
			dc := DryCondition{Kind: c.Kind, Coverage: cover(CoverReplayed)}
			if c.Kind != "time" && c.Kind != "weekday" {
				dc.Coverage = cover(CoverNone, NoteNotRecorded)
			}
			out.Conditions = append(out.Conditions, dc)
		}
	}
	sort.SliceStable(fires, func(i, j int) bool {
		if !fires[i].at.Equal(fires[j].at) {
			return fires[i].at.Before(fires[j].at)
		}
		return fires[i].instance < fires[j].instance
	})
	var runs []time.Time
	alert := s.Trigger.Kind == "alert"
	for _, f := range fires {
		df := DryFiring{Time: f.at, Instance: Mask(clean(f.instance)), Value: f.value, Outcome: "run"}
		var why string
		runs, why = timeGate(s.Options, alert, runs, f.at)
		if why != "" {
			df.Outcome, df.Reason = "skipped", gateCode[why]
		} else {
			// The gate comes first, as in a live run: a firing whose
			// conditions fail still starts the cooldown.
			runs = append(runs, f.at)
			switch held, unknown := dryConditions(s.If, f.at); {
			case held:
				out.Runs++
			case len(unknown) > 0:
				df.Outcome, df.Missing = "unknown", unknown
			default:
				df.Outcome, df.Reason = "skipped", "conditions"
			}
		}
		out.Total++
		if len(out.Firings) < DryRunListed {
			out.Firings = append(out.Firings, df)
		}
	}
	return out, nil
}

// dryConditions evaluates c at t: time and weekday from t, the others not
// recorded. held is true when they hold for sure; otherwise unknown lists
// the conditions nothing recorded that could make them hold (none when
// they fail).
func dryConditions(c *spec.Conditions, t time.Time) (held bool, unknown []string) {
	if c == nil {
		return true, nil
	}
	var known []bool
	var missing []string
	for _, cond := range c.Conditions {
		switch cond.Kind {
		case "time":
			known = append(known, within(t, cond.From, cond.To))
		case "weekday":
			known = append(known, slices.Contains(cond.Days, strings.ToLower(t.Weekday().String()[:3])))
		default:
			if !slices.Contains(missing, cond.Kind) {
				missing = append(missing, cond.Kind)
			}
		}
	}
	if c.Match == "any" {
		if slices.Contains(known, true) {
			return true, nil
		}
		return false, missing
	}
	if slices.Contains(known, false) {
		return false, nil
	}
	return len(missing) == 0, missing
}

// gateCode are timeGate's reasons as codes.
var gateCode = map[string]string{"quiet hours": "quiet", "cooldown": "cooldown", "the hourly maximum was reached": "hourly"}

// drySteps previews each step as an unattended run would: after the first
// step that would wait, every later one waits too.
func (e *Engine) drySteps(ctx context.Context, s spec.Spec) []DryStep {
	out := []DryStep{}
	// one previews a plain step; waiting says whether an earlier step of
	// its path waits, and it returns whether this one does.
	one := func(st spec.Step, branch string, waiting bool) bool {
		ds := DryStep{Step: st.Step, Action: st.Action, Target: Mask(clean(st.Target)), Label: Mask(clean(st.Label)), Outcome: DryUnattended, Branch: branch}
		switch {
		case st.Step == "action":
			ds.Outcome, ds.Reason, ds.Detail = e.dryAction(ctx, st, waiting)
		case waiting:
			ds.Outcome = DryApproval
		}
		out = append(out, ds)
		return ds.Outcome == DryApproval
	}
	waiting := false
	for _, st := range s.Then {
		if st.Step != "check" {
			waiting = one(st, "", waiting) || waiting
			continue
		}
		one(st, "", waiting)
		// Each branch starts from what came before; whichever is taken, a
		// step that waits there makes the rest of the run wait too.
		branchWaits := false
		for _, list := range []struct {
			steps []spec.Step
			label string
		}{{st.ThenDo, "then"}, {st.ElseDo, "else"}} {
			w := waiting
			for _, b := range list.steps {
				w = one(b, list.label, w) || w
			}
			branchWaits = branchWaits || (w && !waiting)
		}
		waiting = waiting || branchWaits
	}
	return out
}

func (e *Engine) dryAction(ctx context.Context, st spec.Step, waiting bool) (outcome, reason, detail string) {
	if e.cfg.Protect != nil {
		if why := e.cfg.Protect(st.Action, st.Target); why != "" {
			return DryRefused, "", clean(why)
		}
	}
	if e.cfg.Registry == nil {
		return DryFail, "no-actions", ""
	}
	plan, ok, err := e.cfg.Registry.PlanUnattended(ctx, st.Action, actions.Request{Target: st.Target, Params: st.Params})
	switch {
	case err != nil:
		return DryFail, "", Mask(clean(err.Error()))
	case actions.Refusal(plan) != "":
		return DryRefused, "", clean(actions.Refusal(plan))
	case waiting:
		return DryApproval, "earlier-waits", ""
	case !ok:
		return DryApproval, "", ""
	}
	return DryUnattended, "", ""
}

// replay lists the trigger's firings in [from, to) from history.
func (e *Engine) replay(ctx context.Context, id int64, t spec.Trigger, from, to time.Time) ([]firing, Coverage, bool, error) {
	h := e.cfg.History
	none := func(note string) ([]firing, Coverage, bool, error) {
		return nil, cover(CoverNone, note), false, nil
	}
	if note, ok := noHistory[t.Kind]; ok {
		return none(note)
	}
	switch t.Kind {
	case "schedule":
		if h.Elapses == nil {
			return none(NoteUnreadable)
		}
		times, trunc, err := h.Elapses(ctx, t.Calendar, from, to)
		if err != nil {
			return none(NoteUnreadable)
		}
		var out []firing
		for _, at := range times {
			if at.After(from) && at.Before(to) {
				out = append(out, firing{at: at})
			}
		}
		return out, cover(CoverReplayed, NoteExact), trunc, nil
	case "login":
		return timesFrom(ctx, h.Boots, from, to, NoteBoots)
	case "resume":
		return timesFrom(ctx, h.Resumes, from, to, "")
	case "internet":
		return e.replayOutages(ctx, t, from, to)
	case "metric":
		return e.replayMetric(ctx, t, from, to)
	case "alert":
		return e.replayEvents(ctx, t, from, to, func(ev store.AlertEvent) bool {
			if id > 0 {
				return ev.RuleID == id
			}
			return ev.Metric == t.Metric && (t.Target == "" || ev.Instance == t.Target)
		}, id > 0)
	}
	if eventMetric[t.Kind] {
		m, _, _ := triggerRule(t)
		return e.replayEvents(ctx, t, from, to, func(ev store.AlertEvent) bool {
			return ev.Metric == m && matches(t, ev.Instance)
		}, false)
	}
	return none(NoteNotRecorded)
}

func timesFrom(ctx context.Context, read func(context.Context) ([]time.Time, error), from, to time.Time, note string) ([]firing, Coverage, bool, error) {
	if read == nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	times, err := read(ctx)
	if err != nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	var out []firing
	for _, at := range times {
		if !at.Before(from) && at.Before(to) {
			out = append(out, firing{at: at})
		}
	}
	c := cover(CoverReplayed)
	if note != "" {
		c.Notes = []string{note}
	}
	return out, c, false, nil
}

func (e *Engine) replayOutages(ctx context.Context, t spec.Trigger, from, to time.Time) ([]firing, Coverage, bool, error) {
	if e.cfg.History.Outages == nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	list, err := e.cfg.History.Outages(ctx, dryRunEvents)
	if err != nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	var out []firing
	in := func(ms int64) bool { at := time.UnixMilli(ms); return !at.Before(from) && at.Before(to) }
	for _, o := range list {
		switch {
		case t.State == "down" && in(o.StartedAt):
			out = append(out, firing{at: time.UnixMilli(o.StartedAt)})
		case t.State == "back" && o.EndedAt != nil && in(*o.EndedAt):
			out = append(out, firing{at: time.UnixMilli(*o.EndedAt), value: float64((*o.EndedAt - o.StartedAt) / 1000)})
		}
	}
	return out, cover(CoverReplayed, NoteOutages), len(list) >= dryRunEvents, nil
}

func (e *Engine) replayEvents(ctx context.Context, t spec.Trigger, from, to time.Time, keep func(store.AlertEvent) bool, exact bool) ([]firing, Coverage, bool, error) {
	if e.cfg.History.Events == nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	list, err := e.cfg.History.Events(ctx, from, to, dryRunEvents)
	if err != nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	var out []firing
	for _, ev := range list {
		if keep(ev) {
			out = append(out, firing{at: time.UnixMilli(ev.StartedAt), instance: ev.Instance, value: ev.Value})
		}
	}
	c := cover(CoverPartial, NoteAlertWatched)
	if exact {
		c = cover(CoverReplayed, NoteAlertOwn)
	}
	return out, c, len(list) >= dryRunEvents, nil
}

// metricSeries are the stored series of each trigger metric: one series,
// or a prefix with instances, and the size that turns it into a percent.
func (e *Engine) metricSeries(ctx context.Context, metric string, from, to time.Time) (map[string][]store.Agg, string, bool, error) {
	h := e.cfg.History
	one := func(name string, total float64) (map[string][]store.Agg, string, bool, error) {
		if h.Series == nil {
			return nil, "", false, errors.New("no series")
		}
		aggs, err := h.Series(ctx, name, from, to)
		if err != nil {
			return nil, "", false, err
		}
		if total > 0 {
			aggs = scaled(aggs, 100/total)
		} else if total < 0 {
			return nil, "", false, errors.New("no size known")
		}
		return map[string][]store.Agg{"": aggs}, "", false, nil
	}
	var tot Totals
	if h.Totals != nil {
		tot = h.Totals()
	}
	orNone := func(v float64) float64 {
		if v <= 0 {
			return -1
		}
		return v
	}
	switch metric {
	case "cpu.usage", "cpu.temp":
		return one(metric, 0)
	case "mem.used_pct":
		return one("mem.used", orNone(tot.Mem))
	case "swap.used_pct":
		return one("swap.used", orNone(tot.Swap))
	case "gpu.temp", "disk.used_pct":
		if h.SeriesLike == nil {
			return nil, "", false, errors.New("no series")
		}
		prefix := map[string]string{"gpu.temp": "gpu.temp", "disk.used_pct": "disk.used"}[metric]
		all, err := h.SeriesLike(ctx, prefix, from, to, dryRunBuckets)
		if err != nil {
			return nil, "", false, err
		}
		n := 0
		for _, a := range all {
			n += len(a)
		}
		if metric == "gpu.temp" {
			return all, "", n >= dryRunBuckets, nil
		}
		out := map[string][]store.Agg{}
		for mount, aggs := range all {
			if size := tot.Disks[mount]; size > 0 {
				out[mount] = scaled(aggs, 100/size)
			}
		}
		return out, NoteDiskSize, n >= dryRunBuckets, nil
	}
	return nil, "", false, errors.New("not recorded")
}

func scaled(aggs []store.Agg, k float64) []store.Agg {
	out := make([]store.Agg, len(aggs))
	for i, a := range aggs {
		a.Avg, a.Min, a.Max = a.Avg*k, a.Min*k, a.Max*k
		out[i] = a
	}
	return out
}

// replayMetric runs the alert evaluator (duration and hysteresis) over the
// minute averages, on a tracker of its own; a gap of more than five
// minutes starts it afresh, as a restart would.
func (e *Engine) replayMetric(ctx context.Context, t spec.Trigger, from, to time.Time) ([]firing, Coverage, bool, error) {
	if t.Metric == "battery.discharging" {
		return nil, cover(CoverNone, NoteNotRecorded), false, nil
	}
	series, note, trunc, err := e.metricSeries(ctx, t.Metric, from, to)
	if err != nil {
		return nil, cover(CoverNone, NoteUnreadable), false, nil
	}
	forS := t.ForS
	if forS > 0 && forS < 60 {
		forS = 60
	}
	rule := store.AlertRule{Metric: t.Metric, Op: t.Op, Threshold: t.Threshold, ForSec: forS}
	c := cover(CoverReplayed, NoteMinute)
	if t.ForS > 0 && t.ForS < 60 {
		c.Notes, c.ForS = append(c.Notes, NoteRounded), t.ForS
	}
	if note != "" {
		c.State, c.Notes = CoverPartial, append(c.Notes, note)
	}
	var out []firing
	var first time.Time
	buckets := 0
	ins := make([]string, 0, len(series))
	for in := range series {
		ins = append(ins, in)
	}
	sort.Strings(ins)
	for _, in := range ins {
		aggs := series[in]
		if !matches(t, in) {
			continue
		}
		buckets += len(aggs)
		if len(aggs) > 0 && (first.IsZero() || aggs[0].Time.Before(first)) {
			first = aggs[0].Time
		}
		tr := &alerts.Tracker{}
		var last time.Time
		for _, a := range aggs {
			if !last.IsZero() && a.Time.Sub(last) > 5*time.Minute {
				tr = &alerts.Tracker{}
			}
			last = a.Time
			// Each bucket is its minute's average, seen at the minute's end.
			at := a.Time.Add(time.Minute)
			if ch, _ := tr.Step(1, rule, in, a.Avg, at); ch == alerts.Opened {
				out = append(out, firing{at: at, instance: in, value: a.Avg})
			}
		}
	}
	if buckets == 0 {
		return nil, cover(CoverNone, NoteNoData), false, nil
	}
	if first.After(from.Add(time.Hour)) {
		c.State, c.Notes, c.Since = CoverPartial, append(c.Notes, NoteSince), &first
	}
	return out, c, trunc, nil
}
