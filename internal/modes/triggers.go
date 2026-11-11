package modes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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
