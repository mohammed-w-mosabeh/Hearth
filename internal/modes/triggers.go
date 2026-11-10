package modes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// ArmDB is what arming reads and writes (store.DB).
type ArmDB interface {
	ModeArmings(ctx context.Context) ([]store.ModeArming, error)
	ArmModeTrigger(ctx context.Context, a store.ModeArming) error
	DisarmModeTrigger(ctx context.Context, modeID string, trigger int) error
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// armRequest is a checked mode/arm or mode/disarm request.
type armRequest struct {
	mode    Mode
	trigger int
}

// RegisterArming adds mode/arm and mode/disarm (target: a mode ID; params
// trigger, its index, and hash, the file's sha256 as the user saw it). A
// trigger is armed only by this click in Hearth, for that version of the
// file: a file that changed since it was shown is refused, and a file that
// changes later disarms it (SECURITY S15b). clock may be nil.
func RegisterArming(reg *actions.Registry, set *Set, db ArmDB, clock func() time.Time) {
	if clock == nil {
		clock = time.Now
	}
	validate := func(r actions.Request) error {
		n, err := strconv.Atoi(r.Params["trigger"])
		if !ValidID(r.Target) || len(r.Params) != 2 || err != nil || n < 0 || n >= maxTriggers || !hashPattern.MatchString(r.Params["hash"]) {
			return errors.New(`target must be a mode ID and params {"trigger": index, "hash": the file's sha256}`)
		}
		return nil
	}
	plan := func(verb string) func(context.Context, actions.Request) (actions.Plan, error) {
		return func(_ context.Context, r actions.Request) (actions.Plan, error) {
			m, ok := set.Mode(r.Target)
			if !ok {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no such mode")
			}
			if m.Hash != r.Params["hash"] {
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "the mode file changed since it was shown; look at it again")
			}
			n, _ := strconv.Atoi(r.Params["trigger"])
			if n >= len(m.Triggers) {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no such trigger")
			}
			label := fmt.Sprintf("%s: %s trigger %d", m.Name, m.Triggers[n].Kind, n+1)
			return actions.Plan{Summary: verb + " " + label, Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: m.ID + "#" + strconv.Itoa(n), Name: label, Detail: m.Triggers[n].Kind}},
				State: armRequest{mode: m, trigger: n}}, nil
		}
	}
	run := func(arm bool) func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
		return func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(armRequest)
			if !ok {
				return actions.Result{}, errors.New("modes: no plan")
			}
			// The file may have changed between the preview and now.
			if cur, ok := set.Mode(st.mode.ID); !ok || cur.Hash != st.mode.Hash {
				return actions.Result{}, actions.Errorf(http.StatusConflict, "the mode file changed since it was shown; look at it again")
			}
			var err error
			if arm {
				err = db.ArmModeTrigger(ctx, store.ModeArming{ModeID: st.mode.ID, Trigger: st.trigger, FileHash: st.mode.Hash, ArmedAt: clock()})
			} else {
				err = db.DisarmModeTrigger(ctx, st.mode.ID, st.trigger)
			}
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}
	}
	reg.Register(actions.Action{Kind: "mode", Name: "arm", Risk: actions.Safe, Validate: validate, Plan: plan("arm"), Run: run(true)})
	reg.Register(actions.Action{Kind: "mode", Name: "disarm", Risk: actions.Safe, Validate: validate, Plan: plan("disarm"), Run: run(false)})
}

// Sources are what the trigger loop reads. A nil source leaves its triggers
// waiting.
type Sources struct {
	// Next is when a calendar expression elapses next, after now.
	Next func(ctx context.Context, onCalendar string) (time.Time, error)
	// Connections are the active NetworkManager connections' UUIDs.
	Connections func(ctx context.Context) (map[string]bool, error)
	// Battery is the battery's charge and whether it is discharging; ok is
	// false without a battery.
	Battery func() (pct float64, discharging, ok bool)
	// Displays is how many displays are connected.
	Displays func() (int, error)
}

// TriggerConfig configures Triggers.
type TriggerConfig struct {
	Set *Set
	DB  interface {
		ModeArmings(context.Context) ([]store.ModeArming, error)
	}
	Sources Sources
	// Activate switches a mode on for a trigger: unattended, no dialog
	// (profiles.Manager.AutoActivate in production). why is for the
	// notification.
	Activate func(ctx context.Context, modeID, by, why string) error
	Clock    func() time.Time
	Log      *slog.Logger
}

// Triggers watches armed triggers and switches their modes on when what
// they wait for happens (ROADMAP 10.6). Each fires on a change, never on
// what it finds when armed or at start: a Wi-Fi connection coming up, the
// battery falling below its level while discharging, a display connected
// or disconnected, a schedule elapsing while hearthd runs (a time missed
// while it was not running does not fire later). The game trigger is the
// profile engine's. A mode a trigger switched on stays on until the user
// reverts it.
type Triggers struct {
	cfg TriggerConfig

	mu    sync.Mutex
	state map[string]*triggerState // by mode ID, trigger index and file hash
}

type triggerState struct {
	next    time.Time // schedule: when it fires next
	active  bool      // wifi: the connection is up; battery: below the level
	screens int       // display: how many were connected
	seen    bool      // the condition was observed once
}

// NewTriggers returns a Triggers; Run starts it.
func NewTriggers(cfg TriggerConfig) *Triggers {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Triggers{cfg: cfg, state: map[string]*triggerState{}}
}

// Run checks the armed triggers every interval until ctx ends.
func (t *Triggers) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Check(ctx)
		}
	}
}

// armed is one armed trigger of the current file.
type armed struct {
	mode Mode
	idx  int
	trig Trigger
	key  string
}

func (t *Triggers) armed(ctx context.Context) ([]armed, error) {
	list, err := t.cfg.DB.ModeArmings(ctx)
	if err != nil {
		return nil, err
	}
	var out []armed
	for _, a := range list {
		m, ok := t.cfg.Set.Mode(a.ModeID)
		if !ok || m.Hash != a.FileHash || a.Trigger >= len(m.Triggers) {
			continue // a changed or broken file is disarmed on its reload
		}
		tr := m.Triggers[a.Trigger]
		if tr.Kind == TriggerGame {
			continue
		}
		out = append(out, armed{mode: m, idx: a.Trigger, trig: tr, key: m.ID + "#" + strconv.Itoa(a.Trigger) + "#" + m.Hash})
	}
	return out, nil
}

// Check looks at every armed trigger once and fires those whose condition
// began since the last look.
func (t *Triggers) Check(ctx context.Context) {
	list, err := t.armed(ctx)
	if err != nil {
		t.cfg.Log.Warn("modes: cannot read armed triggers", "err", err)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	live := map[string]bool{}
	var conns map[string]bool
	var connErr error
	connRead := false
	screens, screenErr, screenRead := 0, error(nil), false
	now := t.cfg.Clock()
	for _, a := range list {
		live[a.key] = true
		st := t.state[a.key]
		if st == nil {
			st = &triggerState{}
			t.state[a.key] = st
		}
		fire, why := false, ""
		switch a.trig.Kind {
		case TriggerSchedule:
			if t.cfg.Sources.Next == nil {
				continue
			}
			if st.seen && !now.Before(st.next) {
				fire, why = true, "its schedule came"
			}
			if !st.seen || fire {
				next, err := t.cfg.Sources.Next(ctx, a.trig.OnCalendar)
				if err != nil {
					t.cfg.Log.Warn("modes: schedule", "mode", a.mode.ID, "err", err)
					continue
				}
				st.next, st.seen = next, true
			}
		case TriggerWiFi:
			if t.cfg.Sources.Connections == nil {
				continue
			}
			if !connRead {
				conns, connErr = t.cfg.Sources.Connections(ctx)
				connRead = true
			}
			if connErr != nil {
				continue
			}
			up := conns[a.trig.Connection]
			fire, why = st.seen && up && !st.active, "a Wi-Fi connection came up"
			st.active, st.seen = up, true
		case TriggerBattery:
			if t.cfg.Sources.Battery == nil {
				continue
			}
			pct, discharging, ok := t.cfg.Sources.Battery()
			if !ok {
				continue
			}
			below := discharging && pct < float64(*a.trig.Below)
			fire, why = st.seen && below && !st.active, fmt.Sprintf("the battery fell below %d%%", *a.trig.Below)
			st.active, st.seen = below, true
		case TriggerDisplay:
			if t.cfg.Sources.Displays == nil {
				continue
			}
			if !screenRead {
				screens, screenErr = t.cfg.Sources.Displays()
				screenRead = true
			}
			if screenErr != nil {
				continue
			}
			switch a.trig.Event {
			case "connect":
				fire, why = st.seen && screens > st.screens, "a display was connected"
			case "disconnect":
				fire, why = st.seen && screens < st.screens, "a display was disconnected"
			}
			st.screens, st.seen = screens, true
		}
		if fire {
			if err := t.cfg.Activate(ctx, a.mode.ID, a.trig.Kind, why); err != nil {
				t.cfg.Log.Warn("modes: trigger", "mode", a.mode.ID, "trigger", a.trig.Kind, "err", err)
			}
		}
	}
	for k := range t.state {
		if !live[k] {
			delete(t.state, k) // disarmed, or its file changed: armed again it starts afresh
		}
	}
}
