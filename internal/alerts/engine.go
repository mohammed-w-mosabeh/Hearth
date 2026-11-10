package alerts

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Topic is the WebSocket topic for incidents opening and closing.
const Topic = "alerts"

// Msg is published on Topic.
type Msg struct {
	Kind  string           `json:"kind"` // "open" or "close"
	Event store.AlertEvent `json:"event"`
}

// Store is what the engine needs from the database (store.DB).
type Store interface {
	AlertRules(ctx context.Context) ([]store.AlertRule, error)
	OpenAlertEvent(ctx context.Context, e store.AlertEvent) (int64, error)
	CloseAlertEvent(ctx context.Context, id int64, endedAt int64, peak float64) error
	OpenAlertEvents(ctx context.Context) ([]store.AlertEvent, error)
}

// Notifier shows a notification (the desktop's, via D-Bus).
type Notifier interface {
	Notify(ctx context.Context, summary, body, severity string) error
}

// Publisher is the subset of api.Hub the engine uses.
type Publisher interface {
	Publish(topic string, data any) error
}

type key struct {
	rule     int64
	instance string
}

// state is one (rule, instance) that is past its threshold.
type state struct {
	since time.Time // when the threshold was first passed
	event *store.AlertEvent
}

// Engine evaluates the enabled rules as values are observed.
type Engine struct {
	store  Store
	notify Notifier
	pub    Publisher
	log    *slog.Logger

	// Tap, if set, sees every observation too (the automations engine,
	// ROADMAP 12.3). Called without the engine's lock held.
	Tap func(ctx context.Context, now time.Time, metric string, values map[string]float64)
	// Opened, if set, is told of each incident that opens, with its
	// description, instead of the engine notifying: the rule's automation
	// runs its steps (ROADMAP 12.7). Called in the background.
	Opened func(ctx context.Context, ev store.AlertEvent, summary, body string)

	mu     sync.Mutex
	rules  []store.AlertRule // enabled only
	states map[key]*state
	seen   map[string]map[string]bool // metric -> instances observed lately
	wg     sync.WaitGroup             // notifications in flight
}

// NewEngine returns an Engine; call Load before observing.
func NewEngine(s Store, n Notifier, p Publisher, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: s, notify: n, pub: p, log: log, states: map[key]*state{}, seen: map[string]map[string]bool{}}
}

// Load reads the rules and resumes incidents left open by a previous run,
// so a restart neither repeats their notification nor loses them.
func (e *Engine) Load(ctx context.Context) error {
	if err := e.Reload(ctx); err != nil {
		return err
	}
	open, err := e.store.OpenAlertEvents(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range open {
		ev := open[i]
		k := key{ev.RuleID, ev.Instance}
		if e.rule(ev.RuleID) == nil {
			e.close(ctx, &ev, time.Now()) // the rule is gone or disabled
			continue
		}
		e.states[k] = &state{since: time.UnixMilli(ev.StartedAt), event: &ev}
	}
	return nil
}

// Reload re-reads the rules after an edit. Incidents of rules that were
// deleted, disabled or changed are closed; a changed rule starts afresh.
func (e *Engine) Reload(ctx context.Context) error {
	all, err := e.store.AlertRules(ctx)
	if err != nil {
		return err
	}
	var enabled []store.AlertRule
	for _, r := range all {
		if r.Enabled && Validate(r) == nil {
			enabled = append(enabled, r)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old := map[int64]store.AlertRule{}
	for _, r := range e.rules {
		old[r.ID] = r
	}
	e.rules = enabled
	now := time.Now()
	for k, st := range e.states {
		r := e.rule(k.rule)
		if r != nil && old[k.rule] == *r {
			continue
		}
		if st.event != nil {
			e.close(ctx, st.event, now)
		}
		delete(e.states, k)
	}
	return nil
}

func (e *Engine) rule(id int64) *store.AlertRule {
	for i := range e.rules {
		if e.rules[i].ID == id {
			return &e.rules[i]
		}
	}
	return nil
}

// Observe evaluates the rules on metric with its current values by
// instance ("" for single-valued metrics). An instance a rule was
// tracking that is no longer present resolves.
func (e *Engine) Observe(ctx context.Context, now time.Time, metric string, values map[string]float64) {
	if e.Tap != nil {
		e.Tap(ctx, now, metric, values)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	inst := make(map[string]bool, len(values))
	for i := range values {
		inst[i] = true
	}
	e.seen[metric] = inst
	for i := range e.rules {
		r := &e.rules[i]
		if r.Metric != metric {
			continue
		}
		for in, v := range values {
			if r.Target != "" && in != r.Target {
				continue
			}
			e.step(ctx, r, in, v, now)
		}
		for k, st := range e.states {
			if k.rule == r.ID {
				if _, ok := values[k.instance]; !ok {
					if st.event != nil {
						e.close(ctx, st.event, now)
					}
					delete(e.states, k)
				}
			}
		}
	}
}

// step moves one (rule, instance) along: ok -> pending -> firing -> ok.
func (e *Engine) step(ctx context.Context, r *store.AlertRule, instance string, v float64, now time.Time) {
	k := key{r.ID, instance}
	st := e.states[k]
	if st != nil && st.event != nil { // firing
		st.event.Peak = worse(*r, st.event.Peak, v)
		if cleared(*r, v) {
			e.close(ctx, st.event, now)
			delete(e.states, k)
		}
		return
	}
	if !breached(*r, v) {
		delete(e.states, k) // pending ends
		return
	}
	if st == nil {
		st = &state{since: now}
		e.states[k] = st
	}
	if now.Sub(st.since) < time.Duration(r.ForSec)*time.Second {
		return
	}
	ev := store.AlertEvent{RuleID: r.ID, RuleName: r.Name, Metric: r.Metric, Instance: instance, Severity: r.Severity,
		Op: r.Op, Threshold: r.Threshold, Value: v, Peak: v, StartedAt: now.UnixMilli(), Notified: !quiet(*r, now)}
	id, err := e.store.OpenAlertEvent(ctx, ev)
	if err != nil {
		e.log.Warn("alerts: record incident", "rule", r.Name, "err", err)
		return // retried on the next observation
	}
	ev.ID = id
	st.event = &ev
	e.publish("open", ev)
	if e.Opened != nil {
		summary, body := Describe(ev)
		opened := e.Opened
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			opened(context.WithoutCancel(ctx), ev, summary, body)
		}()
		return
	}
	if ev.Notified {
		summary, body := Describe(ev)
		e.send(ctx, summary, body, ev.Severity)
	}
}

func (e *Engine) close(ctx context.Context, ev *store.AlertEvent, now time.Time) {
	ended := now.UnixMilli()
	if err := e.store.CloseAlertEvent(ctx, ev.ID, ended, ev.Peak); err != nil {
		e.log.Warn("alerts: close incident", "id", ev.ID, "err", err)
	}
	ev.EndedAt = &ended
	e.publish("close", *ev)
	if m, ok := metric(ev.Metric); ok && m.NotifyEnd && ev.Notified {
		summary, body := DescribeEnd(*ev)
		e.send(ctx, summary, body, "info")
	}
}

// send notifies in the background. It is not cancelled with the
// observation: the notification goes out even if the sample's context ends.
func (e *Engine) send(ctx context.Context, summary, body, severity string) {
	if e.notify == nil {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := e.notify.Notify(nctx, summary, body, severity); err != nil {
			e.log.Warn("alerts: notify", "err", err)
		}
	}()
}

func (e *Engine) publish(kind string, ev store.AlertEvent) {
	if e.pub != nil {
		_ = e.pub.Publish(Topic, Msg{Kind: kind, Event: ev})
	}
}

// Wait blocks until notifications in flight are sent (for shutdown and
// tests).
func (e *Engine) Wait() { e.wg.Wait() }

// Instances returns the instances lately seen for metric, sorted, for the
// rule form's target choices.
func (e *Engine) Instances(metric string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.seen[metric]))
	for i := range e.seen[metric] {
		if i != "" {
			out = append(out, i)
		}
	}
	sort.Strings(out)
	return out
}
