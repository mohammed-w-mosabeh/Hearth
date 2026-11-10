package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// The engine (ROADMAP 12.3, SECURITY S15d): triggers from the alert
// engine's observations and the mode triggers' sources, then the run's
// checks, the conditions and the steps.

// PausedKey is the kill switch's setting.
const PausedKey = "automations.paused"

// Defaults.
const (
	DefaultGlobalLimit = 30 // actions a minute across all automations
	TickEvery          = 15 * time.Second
)

// Run states.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
	RunSkipped = "skipped"
	RunWaiting = "waiting"
	RunLimited = "limited"
)

// Event is what fired a trigger.
type Event struct {
	Kind     string    `json:"kind"`     // the trigger kind
	Instance string    `json:"instance"` // unit, container, mount, connection...
	Value    float64   `json:"value,omitempty"`
	Outage   int64     `json:"outage_s,omitempty"` // internet back: how long it was down
	Detail   string    `json:"detail,omitempty"`
	Time     time.Time `json:"time"`
}

// StepResult is one step of a run.
type StepResult struct {
	Step     string `json:"step"`
	Action   string `json:"action,omitempty"`
	Target   string `json:"target,omitempty"`
	Label    string `json:"label,omitempty"`
	Result   string `json:"result"` // ok, failed, waiting, skipped
	Detail   string `json:"detail,omitempty"`
	Duration int64  `json:"duration_ms"`
}

// RunRecord is a run as stored (cleaned and masked, S19).
type RunRecord struct {
	Trigger    Event        `json:"trigger"`
	Reason     string       `json:"reason,omitempty"` // why it was skipped or limited
	Conditions *bool        `json:"conditions,omitempty"`
	Values     []string     `json:"values,omitempty"` // each condition, in words
	Steps      []StepResult `json:"steps"`
	By         string       `json:"by"` // trigger or test
}

// State answers the conditions' questions; each may be nil (the condition
// is then false, and the run says so).
type State struct {
	ActiveModes    func(ctx context.Context) ([]string, error)
	GameRunning    func() bool
	ContainerState func(ctx context.Context, name string) (string, error) // "running" or "stopped", then "healthy" or "unhealthy"
	UnitState      func(ctx context.Context, scope, name string) (string, error)
}

// Steps runs the steps other than actions (notify, wait, check; ROADMAP
// 12.5 adds notify and check). nil means only actions and waits run.
type StepRunner interface {
	Notify(ctx context.Context, s spec.Step, ev Event, name string) error
	Check(ctx context.Context, s spec.Step) (bool, error)
}

// EngineStore is what the engine needs (store.DB).
type EngineStore interface {
	Automations(ctx context.Context) ([]store.Automation, error)
	SaveAutomationRun(ctx context.Context, r store.AutomationRun) (int64, error)
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Config configures the engine.
type Config struct {
	Store    EngineStore
	Registry *actions.Registry
	Sources  modes.Sources
	Watch    Watch
	State    State
	Steps    StepRunner
	// Notify tells the user when the global limit pauses everything.
	Notify      func(ctx context.Context, summary, body string)
	GlobalLimit int
	Clock       func() time.Time
	Log         *slog.Logger
	// Sleep waits in a wait step (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
}

type loaded struct {
	store.Automation
	spec spec.Spec
}

// pollState is what a polled trigger saw last.
type pollState struct {
	seen     bool
	next     time.Time
	up       bool
	on       bool
	displays int
}

// Engine evaluates the enabled automations.
type Engine struct {
	cfg     Config
	tracker alerts.Tracker

	mu      sync.Mutex
	autos   []loaded
	running map[int64]bool
	runs    map[int64][]time.Time          // run starts in the last hour
	tags    map[int64]map[string]time.Time // instances the automation's own actions touched, until
	poll    map[int64]*pollState
	acts    []time.Time // actions in the last minute, all automations
	src     map[int64]*sourceState
	files   *fileWatch
	runCtx  context.Context // Run's, for the file watches set up on reload
	wg      sync.WaitGroup
}

// NewEngine returns an engine; Load before use.
func NewEngine(c Config) *Engine {
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.GlobalLimit == 0 {
		c.GlobalLimit = DefaultGlobalLimit
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	return &Engine{cfg: c, running: map[int64]bool{}, runs: map[int64][]time.Time{}, tags: map[int64]map[string]time.Time{},
		poll: map[int64]*pollState{}}
}

// Load reads the enabled automations; a spec that no longer validates is
// left out (and shown as invalid by the API).
func (e *Engine) Load(ctx context.Context) error {
	all, err := e.cfg.Store.Automations(ctx)
	if err != nil {
		return err
	}
	var list []loaded
	for _, a := range all {
		if !a.Enabled {
			continue
		}
		s, err := spec.Parse(a.Spec)
		if err != nil {
			e.cfg.Log.Warn("automations: invalid spec left out", "id", a.ID, "err", err)
			continue
		}
		list = append(list, loaded{Automation: a, spec: s})
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	keep := map[int64]bool{}
	for _, a := range list {
		keep[a.ID] = true
		for _, old := range e.autos {
			if old.ID == a.ID && string(old.Spec) != string(a.Spec) {
				e.tracker.Forget(a.ID)
				delete(e.poll, a.ID)
			}
		}
	}
	for _, old := range e.autos {
		if !keep[old.ID] {
			e.tracker.Forget(old.ID)
			delete(e.poll, old.ID)
		}
	}
	e.autos = list
	for id := range e.src {
		if !keep[id] {
			delete(e.src, id)
		}
	}
	runCtx := e.runCtx
	e.mu.Unlock()
	if runCtx != nil {
		e.watchFiles(runCtx, list)
	}
	e.mu.Lock()
	return nil
}

// Paused reports the kill switch.
func (e *Engine) Paused(ctx context.Context) bool {
	raw, ok, err := e.cfg.Store.Setting(ctx, PausedKey)
	if err != nil {
		return true // fail closed: an unreadable switch pauses
	}
	return ok && string(raw) == "true"
}

// SetPaused sets the kill switch, audited.
func (e *Engine) SetPaused(ctx context.Context, on bool, by string) error {
	if err := e.cfg.Store.SetSetting(ctx, PausedKey, json.RawMessage(strconv.FormatBool(on))); err != nil {
		return err
	}
	_ = e.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, Action: "automations.pause", Target: "automations",
		Result: fmt.Sprintf("paused=%t by %s", on, by)})
	return nil
}

// triggerRule maps the triggers fed by the alert engine to a rule the
// tracker evaluates: metric thresholds, and events reported as 0/1.
func triggerRule(t spec.Trigger) (metric string, rule store.AlertRule, ok bool) {
	event := store.AlertRule{Op: ">", Threshold: 0.5}
	switch t.Kind {
	case "metric":
		return t.Metric, store.AlertRule{Metric: t.Metric, Op: t.Op, Threshold: t.Threshold, ForSec: t.ForS}, true
	case "container":
		m := map[string]string{"died": "container.exited", "unhealthy": "container.unhealthy", "oom": "container.oom"}[t.Event]
		event.Metric = m
		return m, event, true
	case "unit_failed":
		event.Metric = "unit.failed"
		return "unit.failed", event, true
	case "new_listener":
		event.Metric = "net.new_listener"
		return "net.new_listener", event, true
	case "usb_device":
		event.Metric = "usb.new_device"
		return "usb.new_device", event, true
	case "integrity_changed":
		event.Metric = "integrity.changed"
		return "integrity.changed", event, true
	case "internet":
		event.Metric = "net.internet_down"
		return "net.internet_down", event, true
	}
	return "", store.AlertRule{}, false
}

// matches reports whether instance is the trigger's target.
func matches(t spec.Trigger, instance string) bool {
	switch t.Kind {
	case "unit_failed":
		scope, name, _ := strings.Cut(instance, "/")
		return (t.Scope == "" || t.Scope == scope) && (t.Target == "" || t.Target == name)
	default:
		return t.Target == "" || t.Target == instance
	}
}

// Observe is the alert engine's tap: every observation, evaluated against
// the triggers it feeds.
func (e *Engine) Observe(ctx context.Context, now time.Time, metric string, values map[string]float64) {
	e.mu.Lock()
	autos := slices.Clone(e.autos)
	e.mu.Unlock()
	for _, a := range autos {
		m, rule, ok := triggerRule(a.spec.Trigger)
		if !ok || m != metric {
			continue
		}
		for in, v := range values {
			if !matches(a.spec.Trigger, in) {
				continue
			}
			ch, dur := e.tracker.Step(a.ID, rule, in, v, now)
			ev := Event{Kind: a.spec.Trigger.Kind, Instance: in, Value: v, Time: now}
			switch {
			case a.spec.Trigger.Kind == "internet":
				if (ch == alerts.Opened && a.spec.Trigger.State == "down") || (ch == alerts.Closed && a.spec.Trigger.State == "back") {
					ev.Outage = int64(dur / time.Second)
					e.Fire(ctx, a, ev, "trigger")
				}
			case ch == alerts.Opened:
				e.Fire(ctx, a, ev, "trigger")
			}
		}
		e.tracker.Gone(a.ID, values)
	}
}

// Tick looks at the polled triggers once: schedules, Wi-Fi and VPN,
// power, displays. Each fires on a change, never on what it first finds.
func (e *Engine) Tick(ctx context.Context) {
	e.mu.Lock()
	autos := slices.Clone(e.autos)
	e.mu.Unlock()
	now := e.cfg.Clock()
	src := e.cfg.Sources
	var conns map[string]bool
	connsRead := false
	for _, a := range autos {
		t := a.spec.Trigger
		e.mu.Lock()
		st := e.poll[a.ID]
		if st == nil {
			st = &pollState{}
			e.poll[a.ID] = st
		}
		e.mu.Unlock()
		var ev *Event
		switch t.Kind {
		case "schedule":
			if src.Next == nil {
				continue
			}
			due := st.seen && !now.Before(st.next)
			if !st.seen || due {
				next, err := src.Next(ctx, t.Calendar)
				if err != nil {
					continue
				}
				st.next, st.seen = next, true
			}
			if due {
				ev = &Event{Kind: "schedule", Instance: t.Calendar, Time: now}
			}
		case "wifi", "vpn":
			if src.Connections == nil {
				continue
			}
			if !connsRead {
				conns, _ = src.Connections(ctx)
				connsRead = true
			}
			if conns == nil {
				continue
			}
			up := conns[t.UUID]
			if st.seen && up != st.up && up == (t.State == "up") {
				ev = &Event{Kind: t.Kind, Instance: t.UUID, Time: now}
			}
			st.up, st.seen = up, true
		case "power":
			if src.Battery == nil {
				continue
			}
			_, discharging, ok := src.Battery()
			if !ok {
				continue
			}
			if st.seen && discharging != st.on && discharging == (t.Event == "unplugged") {
				ev = &Event{Kind: "power", Instance: t.Event, Time: now}
			}
			st.on, st.seen = discharging, true
		case "display":
			if src.Displays == nil {
				continue
			}
			n, err := src.Displays()
			if err != nil {
				continue
			}
			if st.seen && ((t.Event == "connected" && n > st.displays) || (t.Event == "disconnected" && n < st.displays)) {
				ev = &Event{Kind: "display", Instance: t.Event, Value: float64(n), Time: now}
			}
			st.displays, st.seen = n, true
		}
		if ev != nil {
			e.Fire(ctx, a, *ev, "trigger")
		}
	}
	e.tickSources(ctx, autos, now)
}

// Login fires the login triggers once (hearthd starts with the session).
func (e *Engine) Login(ctx context.Context) {
	e.mu.Lock()
	autos := slices.Clone(e.autos)
	e.mu.Unlock()
	for _, a := range autos {
		if a.spec.Trigger.Kind == "login" {
			e.Fire(ctx, a, Event{Kind: "login", Time: e.cfg.Clock()}, "trigger")
		}
	}
}

// Run loads, fires the login triggers, and polls until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	e.mu.Unlock()
	e.watchResume(ctx)
	if err := e.Load(ctx); err != nil {
		e.cfg.Log.Warn("automations: load", "err", err)
	}
	e.Login(ctx)
	t := time.NewTicker(TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.wg.Wait()
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

// Wait blocks until the runs in flight end.
func (e *Engine) Wait() { e.wg.Wait() }

func clockMinutes(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m
}

// within reports whether now's time of day is in [from, to), across
// midnight when to is earlier.
func within(now time.Time, from, to string) bool {
	m := now.Hour()*60 + now.Minute()
	f, t := clockMinutes(from), clockMinutes(to)
	if f < t {
		return m >= f && m < t
	}
	return m >= f || m < t
}

// tagged reports whether the automation's own actions touched instance
// within its cooldown (it does not trigger itself).
func (e *Engine) tagged(id int64, instance string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	until, ok := e.tags[id][instance]
	return ok && now.Before(until)
}

func (e *Engine) tag(id int64, instance string, until time.Time) {
	if instance == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tags[id] == nil {
		e.tags[id] = map[string]time.Time{}
	}
	e.tags[id][instance] = until
	// "system/x.service" also tags "x.service" and the reverse.
	if _, name, ok := strings.Cut(instance, "/"); ok {
		e.tags[id][name] = until
	}
}

// gate decides whether a firing becomes a run: the kill switch, the global
// limit, quiet hours, the cooldown and the hourly maximum, in that order.
// It reserves the run when it may go.
func (e *Engine) gate(ctx context.Context, a loaded, now time.Time) (state, reason string) {
	if e.Paused(ctx) {
		return RunSkipped, "all automations are paused"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cut := now.Add(-time.Minute)
	e.acts = slices.DeleteFunc(e.acts, func(t time.Time) bool { return t.Before(cut) })
	if len(e.acts) >= e.cfg.GlobalLimit {
		return RunLimited, "the global limit was reached"
	}
	o := a.spec.Options
	if o.QuietFrom != "" && within(now, o.QuietFrom, o.QuietTo) {
		return RunSkipped, "quiet hours"
	}
	if e.running[a.ID] {
		return RunSkipped, "a run is still going"
	}
	hour := now.Add(-time.Hour)
	e.runs[a.ID] = slices.DeleteFunc(e.runs[a.ID], func(t time.Time) bool { return t.Before(hour) })
	if r := e.runs[a.ID]; len(r) > 0 && now.Sub(r[len(r)-1]) < time.Duration(o.CooldownS)*time.Second {
		return RunSkipped, "cooldown"
	}
	if len(e.runs[a.ID]) >= o.MaxPerHour {
		return RunSkipped, "the hourly maximum was reached"
	}
	e.runs[a.ID] = append(e.runs[a.ID], now)
	e.running[a.ID] = true
	return "", ""
}

// Fire starts a run of a for ev, unless its own actions caused ev. The run
// goes on in the background; gate's refusals are recorded.
func (e *Engine) Fire(ctx context.Context, a loaded, ev Event, by string) {
	now := e.cfg.Clock()
	if by == "trigger" && e.tagged(a.ID, ev.Instance, now) {
		return
	}
	rec := RunRecord{Trigger: ev, By: by, Steps: []StepResult{}}
	if state, reason := e.gate(ctx, a, now); state != "" {
		rec.Reason = reason
		e.save(ctx, a.ID, now, now, state, rec)
		if state == RunLimited {
			e.limitReached(ctx)
		}
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		rctx := context.WithoutCancel(ctx)
		state := e.run(rctx, a, &rec)
		e.mu.Lock()
		delete(e.running, a.ID)
		e.mu.Unlock()
		e.save(rctx, a.ID, now, e.cfg.Clock(), state, rec)
		_ = e.cfg.Store.Audit(rctx, store.AuditEntry{Origin: store.Origin("automation:" + strconv.FormatInt(a.ID, 10)),
			Action: "automation.run", Target: "automation:" + strconv.FormatInt(a.ID, 10), Result: state})
	}()
}

// limitReached pauses everything and tells the user, once.
func (e *Engine) limitReached(ctx context.Context) {
	if e.Paused(ctx) {
		return
	}
	if err := e.SetPaused(ctx, true, "global limit"); err != nil {
		e.cfg.Log.Warn("automations: pause", "err", err)
	}
	if e.cfg.Notify != nil {
		e.cfg.Notify(ctx, "Automations paused",
			fmt.Sprintf("More than %d actions ran in a minute, so every automation is paused. Resume them in Hearth.", e.cfg.GlobalLimit))
	}
}

func (e *Engine) save(ctx context.Context, id int64, started, finished time.Time, state string, rec RunRecord) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if _, err := e.cfg.Store.SaveAutomationRun(ctx, store.AutomationRun{AutomationID: id, Started: started.UnixMilli(),
		Finished: finished.UnixMilli(), State: state, Record: raw}); err != nil {
		e.cfg.Log.Warn("automations: record run", "id", id, "err", err)
	}
}

// conditions evaluates the IF part; values says each in words.
func (e *Engine) conditions(ctx context.Context, c *spec.Conditions, now time.Time) (bool, []string) {
	if c == nil {
		return true, nil
	}
	var values []string
	results := make([]bool, 0, len(c.Conditions))
	for _, cond := range c.Conditions {
		ok, word := e.condition(ctx, cond, now)
		results = append(results, ok)
		values = append(values, fmt.Sprintf("%s: %t (%s)", cond.Kind, ok, word))
	}
	if c.Match == "any" {
		return slices.Contains(results, true), values
	}
	return !slices.Contains(results, false), values
}

func (e *Engine) condition(ctx context.Context, c spec.Condition, now time.Time) (bool, string) {
	src, st := e.cfg.Sources, e.cfg.State
	switch c.Kind {
	case "time":
		return within(now, c.From, c.To), now.Format("15:04")
	case "weekday":
		day := strings.ToLower(now.Weekday().String()[:3])
		return slices.Contains(c.Days, day), day
	case "power", "battery":
		if src.Battery == nil {
			return false, "no battery source"
		}
		pct, discharging, ok := src.Battery()
		if !ok {
			return c.Kind == "power" && c.On == "ac", "no battery"
		}
		if c.Kind == "power" {
			return discharging == (c.On == "battery"), map[bool]string{true: "on battery", false: "on AC"}[discharging]
		}
		if c.Op == "above" {
			return pct > float64(c.Percent), fmt.Sprintf("%.0f%%", pct)
		}
		return pct < float64(c.Percent), fmt.Sprintf("%.0f%%", pct)
	case "wifi":
		if src.Connections == nil {
			return false, "no network source"
		}
		conns, err := src.Connections(ctx)
		if err != nil {
			return false, "connections could not be read"
		}
		return conns[c.UUID] == c.Is, map[bool]string{true: "connected", false: "not connected"}[conns[c.UUID]]
	case "mode":
		if st.ActiveModes == nil {
			return false, "no mode source"
		}
		active, err := st.ActiveModes(ctx)
		if err != nil {
			return false, "modes could not be read"
		}
		on := slices.Contains(active, c.Mode)
		return on == c.Is, map[bool]string{true: "active", false: "not active"}[on]
	case "game":
		if st.GameRunning == nil {
			return false, "no game source"
		}
		on := st.GameRunning()
		return on == c.Is, map[bool]string{true: "a game runs", false: "no game runs"}[on]
	case "container":
		if st.ContainerState == nil {
			return false, "no container source"
		}
		got, err := st.ContainerState(ctx, c.Target)
		if err != nil {
			return false, "state could not be read"
		}
		return slices.Contains(strings.Fields(got), c.State), got
	case "unit":
		if st.UnitState == nil {
			return false, "no unit source"
		}
		got, err := st.UnitState(ctx, c.Scope, c.Target)
		if err != nil {
			return false, "state could not be read"
		}
		return got == c.State, got
	}
	return false, "unknown"
}

// run evaluates the conditions and runs the steps.
func (e *Engine) run(ctx context.Context, a loaded, rec *RunRecord) string {
	now := e.cfg.Clock()
	ok, values := e.conditions(ctx, a.spec.If, now)
	if a.spec.If != nil {
		rec.Conditions, rec.Values = &ok, values
	}
	if !ok {
		rec.Reason = "conditions did not hold"
		return RunSkipped
	}
	return e.steps(ctx, a, a.spec.Then, rec)
}

// steps runs steps in order; the first that cannot run unattended parks
// the rest (ROADMAP 12.6), a failure stops the run unless it may continue.
func (e *Engine) steps(ctx context.Context, a loaded, steps []spec.Step, rec *RunRecord) string {
	by := "automation:" + strconv.FormatInt(a.ID, 10)
	for i, st := range steps {
		start := e.cfg.Clock()
		res := StepResult{Step: st.Step, Action: st.Action, Target: st.Target, Label: st.Label, Result: "ok"}
		var err error
		switch st.Step {
		case "action":
			e.mu.Lock()
			e.acts = append(e.acts, start)
			over := len(e.acts) > e.cfg.GlobalLimit
			e.mu.Unlock()
			if over {
				res.Result, res.Detail = "skipped", "the global limit was reached"
				rec.Steps = append(rec.Steps, res)
				e.limitReached(ctx)
				return RunLimited
			}
			var u actions.Unattended
			u, err = e.cfg.Registry.RunUnattended(ctx, st.Action, actions.Request{Target: st.Target, Params: st.Params}, by)
			if errors.Is(err, actions.ErrNeedsApproval) {
				res.Result, res.Detail = "waiting", "needs your approval"
				rec.Steps = append(rec.Steps, res)
				for _, rest := range steps[i+1:] {
					rec.Steps = append(rec.Steps, StepResult{Step: rest.Step, Action: rest.Action, Target: rest.Target, Label: rest.Label, Result: "waiting"})
				}
				return RunWaiting
			}
			if err == nil && len(u.Result.Failed) > 0 {
				err = fmt.Errorf("%s: %s", u.Result.Failed[0].Reason, u.Result.Failed[0].Detail)
			}
			// What the action touched does not trigger this automation again
			// within its cooldown.
			e.tag(a.ID, st.Target, e.cfg.Clock().Add(time.Duration(a.spec.Options.CooldownS)*time.Second))
		case "wait":
			err = e.cfg.Sleep(ctx, time.Duration(st.Seconds)*time.Second)
		case "notify":
			if e.cfg.Steps == nil {
				err = errors.New("notifications are not available")
			} else {
				err = e.cfg.Steps.Notify(ctx, st, rec.Trigger, a.Name)
			}
		case "check":
			var pass bool
			if e.cfg.Steps == nil {
				err = errors.New("checks are not available")
			} else if pass, err = e.cfg.Steps.Check(ctx, st); err == nil {
				res.Detail = map[bool]string{true: "then", false: "else"}[pass]
				res.Duration = e.cfg.Clock().Sub(start).Milliseconds()
				rec.Steps = append(rec.Steps, res)
				branch := st.ElseDo
				if pass {
					branch = st.ThenDo
				}
				if state := e.steps(ctx, a, branch, rec); state != RunDone {
					return state
				}
				continue
			}
		}
		res.Duration = e.cfg.Clock().Sub(start).Milliseconds()
		if err != nil {
			res.Result, res.Detail = "failed", clean(err.Error())
			rec.Steps = append(rec.Steps, res)
			if st.Step == "action" && st.Continue && e.mayContinue(st) {
				continue
			}
			return RunFailed
		}
		rec.Steps = append(rec.Steps, res)
	}
	return RunDone
}

// mayContinue: "continue on failure" is allowed only for steps that run
// unattended and are safe.
func (e *Engine) mayContinue(st spec.Step) bool {
	a, ok := e.cfg.Registry.Lookup(st.Action)
	return ok && a.Risk == actions.Safe && e.cfg.Registry.AuthorityOf(st.Action, actions.Request{Target: st.Target}) == actions.AuthorityNone
}

// clean drops control characters and bounds a detail (S19: details come
// from errors, never from secrets).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
