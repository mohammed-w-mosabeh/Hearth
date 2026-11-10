package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Armed mode triggers are automations (ROADMAP 12.7, docs/plan/automations.md
// §7): each trigger armed by a click for a version of its file is one
// automation "WHEN trigger THEN mode/activate", owned by that file and that
// hash. mode_arming stays the record of what the user armed; Sync makes the
// automations match it, so a disarmed trigger, or one whose file changed,
// has its automation deleted. A mode file still cannot arm anything by
// itself, and mode/activate refuses a file that changed (SECURITY S15b).

// OriginPrefix starts the origin of a mode trigger's automation.
const OriginPrefix = "mode:"

// Origin is the origin of trigger idx of mode id for the file with hash.
func Origin(id string, idx int, hash string) string {
	return OriginPrefix + id + "#" + strconv.Itoa(idx) + "@" + hash
}

// why is what a notification says switched the mode on, as the trigger
// loop of 10.6 said it.
func why(t Trigger) string {
	switch t.Kind {
	case TriggerSchedule:
		return "its schedule came"
	case TriggerWiFi:
		return "a Wi-Fi connection came up"
	case TriggerBattery:
		if t.Below != nil {
			return fmt.Sprintf("the battery fell below %d%%", *t.Below)
		}
	case TriggerDisplay:
		if t.Event == "disconnect" {
			return "a display was disconnected"
		}
		return "a display was connected"
	case TriggerGame:
		return "a game started"
	}
	return "its trigger fired"
}

// TriggerSpec is the automation of trigger idx of m.
func TriggerSpec(m Mode, idx int) (spec.Spec, error) {
	if idx < 0 || idx >= len(m.Triggers) {
		return spec.Spec{}, errors.New("no such trigger")
	}
	t := m.Triggers[idx]
	var tr spec.Trigger
	switch t.Kind {
	case TriggerSchedule:
		tr = spec.Trigger{Kind: "schedule", Calendar: t.OnCalendar}
	case TriggerWiFi:
		tr = spec.Trigger{Kind: "wifi", UUID: t.Connection, State: "up"}
	case TriggerBattery:
		if t.Below == nil {
			return spec.Spec{}, errors.New("battery trigger without a level")
		}
		// Reported only while the battery discharges.
		tr = spec.Trigger{Kind: "metric", Metric: "battery.discharging", Op: "<", Threshold: float64(*t.Below)}
	case TriggerDisplay:
		tr = spec.Trigger{Kind: "display", Event: map[string]string{"connect": "connected", "disconnect": "disconnected"}[t.Event]}
	case TriggerGame:
		tr = spec.Trigger{Kind: "app", State: "started", Game: true}
	default:
		return spec.Spec{}, fmt.Errorf("unknown trigger kind %q", t.Kind)
	}
	return spec.Spec{
		Trigger: tr,
		Then: []spec.Step{{Step: "action", Action: "mode/activate", Target: m.ID,
			Params: map[string]string{"trigger": strconv.Itoa(idx), "hash": m.Hash}}},
		Options: spec.Options{CooldownS: spec.DefaultCool, MaxPerHour: spec.DefaultPerHour},
	}, nil
}

// triggerName names a trigger's automation within spec.MaxName.
func triggerName(m Mode, idx int) string {
	suffix := fmt.Sprintf(": %s trigger %d", m.Triggers[idx].Kind, idx+1)
	name := m.Name
	if r := []rune(name); len(r)+len([]rune(suffix)) > spec.MaxName {
		name = string(r[:max(1, spec.MaxName-len([]rune(suffix)))])
	}
	return name + suffix
}

// SyncDB is what Sync reads and writes (store.DB).
type SyncDB interface {
	ModeArmings(ctx context.Context) ([]store.ModeArming, error)
	SyncModeAutomations(ctx context.Context, desired []store.Automation) (created, deleted int, err error)
}

// Sync makes the mode triggers' automations match what is armed for the
// current files, in one transaction. It reports whether anything changed.
// A trigger whose automation does not pass the validator is kept turned
// off, so the Automations page shows why.
func Sync(ctx context.Context, db SyncDB, set *Set) (bool, error) {
	armings, err := db.ModeArmings(ctx)
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	desired := []store.Automation{}
	for _, a := range armings {
		m, ok := set.Mode(a.ModeID)
		if !ok || m.Hash != a.FileHash || a.Trigger >= len(m.Triggers) {
			continue // a changed or broken file: its triggers are disarmed on reload
		}
		s, err := TriggerSpec(m, a.Trigger)
		if err != nil {
			continue
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return false, err
		}
		_, perr := spec.Parse(raw)
		desired = append(desired, store.Automation{Name: triggerName(m, a.Trigger), Enabled: perr == nil, Spec: raw,
			Origin: Origin(m.ID, a.Trigger, m.Hash), Created: now, Updated: now})
	}
	c, d, err := db.SyncModeAutomations(ctx, desired)
	return c+d > 0, err
}

// Syncer runs Sync when asked and on a tick, and tells Changed after a
// change (the automations engine reloads).
type Syncer struct {
	DB      SyncDB
	Set     *Set
	Changed func()
	Log     *slog.Logger

	mu sync.Mutex
}

// Now syncs once.
func (s *Syncer) Now(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, err := Sync(ctx, s.DB, s.Set)
	if err != nil && s.Log != nil {
		s.Log.Warn("modes: cannot sync trigger automations", "err", err)
	}
	if changed && s.Changed != nil {
		s.Changed()
	}
}

// Run syncs every interval until ctx ends: every writer of mode_arming
// (arming, saving a mode with its game trigger, migration) is covered.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.Now(ctx)
		}
	}
}

// activateRequest is a checked mode/activate request.
type activateRequest struct {
	mode    Mode
	trigger Trigger
}

// RegisterActivate adds mode/activate (target: a mode ID; params trigger,
// its index, and hash, the file's sha256 when it was armed): the automatic
// switch of an armed trigger, as 10.6 made it: only what can run
// unattended, stack steps left as approvals, never a dialog (activate is
// profiles.Manager.AutoActivate in production). A file that changed since
// it was armed is refused. Only a mode trigger's automation may hold it
// (automations.API refuses it in any other).
func RegisterActivate(reg *actions.Registry, set *Set, activate func(ctx context.Context, modeID, by, why string) error) {
	validate := func(r actions.Request) error {
		n, err := strconv.Atoi(r.Params["trigger"])
		if !ValidID(r.Target) || len(r.Params) != 2 || err != nil || n < 0 || n >= maxTriggers || !hashPattern.MatchString(r.Params["hash"]) {
			return errors.New(`target must be a mode ID and params {"trigger": index, "hash": the file's sha256}`)
		}
		return nil
	}
	current := func(r actions.Request) (activateRequest, error) {
		m, ok := set.Mode(r.Target)
		if !ok {
			return activateRequest{}, actions.Errorf(http.StatusNotFound, "no such mode")
		}
		if m.Hash != r.Params["hash"] {
			return activateRequest{}, actions.Errorf(http.StatusConflict, "the mode file changed since its trigger was armed")
		}
		n, _ := strconv.Atoi(r.Params["trigger"])
		if n >= len(m.Triggers) {
			return activateRequest{}, actions.Errorf(http.StatusNotFound, "no such trigger")
		}
		return activateRequest{mode: m, trigger: m.Triggers[n]}, nil
	}
	reg.Register(actions.Action{Kind: "mode", Name: "activate", Risk: actions.Reversible, Authority: actions.AuthorityNone,
		Validate: validate,
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			st, err := current(r)
			if err != nil {
				return actions.Plan{}, err
			}
			return actions.Plan{Summary: "switch " + st.mode.Name + " on", Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: st.mode.ID, Name: st.mode.Name, Detail: "what can run unattended; stacks wait for your approval"}},
				State: st}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			// The file may have changed between the preview and now.
			st, err := current(r)
			if err != nil {
				return actions.Result{}, err
			}
			if prev, ok := p.State.(activateRequest); !ok || prev.mode.Hash != st.mode.Hash {
				return actions.Result{}, errors.New("modes: no plan")
			}
			if err := activate(ctx, st.mode.ID, st.trigger.Kind, why(st.trigger)); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}})
}

// IsTriggerOrigin reports whether origin is a mode trigger's.
func IsTriggerOrigin(origin string) bool { return strings.HasPrefix(origin, OriginPrefix) }

// Arming wraps db so that arming and disarming sync at once.
func (s *Syncer) Arming(db ArmDB) ArmDB { return syncArm{db, s} }

type syncArm struct {
	ArmDB
	s *Syncer
}

func (a syncArm) ArmModeTrigger(ctx context.Context, m store.ModeArming) error {
	if err := a.ArmDB.ArmModeTrigger(ctx, m); err != nil {
		return err
	}
	a.s.Now(context.WithoutCancel(ctx))
	return nil
}

func (a syncArm) DisarmModeTrigger(ctx context.Context, id string, trigger int) error {
	if err := a.ArmDB.DisarmModeTrigger(ctx, id, trigger); err != nil {
		return err
	}
	a.s.Now(context.WithoutCancel(ctx))
	return nil
}
