package alerts

import (
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Tracker is the rule evaluator without incidents in the store: a
// threshold passed for its duration opens, back past the metric's
// hysteresis closes (ok -> pending -> firing -> ok). The automations
// engine evaluates its metric triggers with it, exactly as rules are
// evaluated (ROADMAP 12.3, SECURITY S15d).
type Tracker struct {
	mu     sync.Mutex
	states map[tkey]*tstate
}

type tkey struct {
	id       int64
	instance string
}

type tstate struct {
	since  time.Time
	firing bool
	opened time.Time
}

// Change is what one step did.
type Change int

const (
	// Unchanged: nothing to report.
	Unchanged Change = iota
	// Opened: the threshold was passed for the rule's duration.
	Opened
	// Closed: a firing one is back to normal; Since is how long it fired.
	Closed
)

// Step evaluates rule id's value v for instance at now.
func (t *Tracker) Step(id int64, r store.AlertRule, instance string, v float64, now time.Time) (Change, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.states == nil {
		t.states = map[tkey]*tstate{}
	}
	k := tkey{id, instance}
	st := t.states[k]
	if st != nil && st.firing {
		if cleared(r, v) {
			delete(t.states, k)
			return Closed, now.Sub(st.opened)
		}
		return Unchanged, 0
	}
	if !breached(r, v) {
		delete(t.states, k)
		return Unchanged, 0
	}
	if st == nil {
		st = &tstate{since: now}
		t.states[k] = st
	}
	if now.Sub(st.since) < time.Duration(r.ForSec)*time.Second {
		return Unchanged, 0
	}
	st.firing, st.opened = true, now
	return Opened, 0
}

// Gone forgets instances of id not in present (they went away: no event).
func (t *Tracker) Gone(id int64, present map[string]float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.states {
		if k.id == id {
			if _, ok := present[k.instance]; !ok {
				delete(t.states, k)
			}
		}
	}
}

// Forget drops every state of id (its automation changed or went).
func (t *Tracker) Forget(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.states {
		if k.id == id {
			delete(t.states, k)
		}
	}
}
