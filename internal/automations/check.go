package automations

import (
	"context"
	"errors"
	"fmt"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

// CheckDeps is what a save check reads from the machine.
type CheckDeps struct {
	// Registry previews each action step; nil checks names only (Known).
	Registry *actions.Registry
	// Known says whether an action exists, when there is no Registry.
	Known func(action string) bool
	// Calendar asks systemd whether a schedule means something.
	Calendar func(ctx context.Context, expr string) error
	// Protect names why a target is protected beyond what an action's own
	// preview blocks (the running kernel and the two newest), or "".
	Protect func(action, target string) string
}

// NewChecker checks what a spec cannot know by itself (SECURITY S15d):
// every action is in the registry and its parameters valid; no step needs
// a typed confirmation or aims at a protected target, by the action's own
// preview now; "continue" is set only on safe steps that never ask for a
// password; a schedule means something to systemd. The engine checks the
// targets again before each run.
func NewChecker(d CheckDeps) Checker {
	return func(ctx context.Context, s spec.Spec) error {
		var err error
		s.Steps(func(st spec.Step) {
			if err == nil && st.Step == "action" {
				err = d.checkStep(ctx, st)
			}
		})
		if err != nil {
			return err
		}
		if s.Trigger.Kind == "schedule" && d.Calendar != nil {
			if err := d.Calendar(ctx, s.Trigger.Calendar); err != nil {
				return fmt.Errorf("schedule: %w", err)
			}
		}
		return nil
	}
}

func (d CheckDeps) checkStep(ctx context.Context, st spec.Step) error {
	if d.Registry == nil {
		if d.Known != nil && !d.Known(st.Action) {
			return fmt.Errorf("%s is not an action Hearth has", st.Action)
		}
		return nil
	}
	a, ok := d.Registry.Lookup(st.Action)
	if !ok {
		return fmt.Errorf("%s is not an action Hearth has", st.Action)
	}
	if why := d.protect(st.Action, st.Target); why != "" {
		return fmt.Errorf("%s %s: %s", st.Action, st.Target, why)
	}
	req := actions.Request{Target: st.Target, Params: st.Params}
	if st.Continue && (a.Risk != actions.Safe || d.Registry.AuthorityOf(st.Action, req) != actions.AuthorityNone) {
		return fmt.Errorf("%s: continue on failure is only for safe steps that never ask for a password", st.Action)
	}
	// The preview runs under the unattended mark: a save never asks polkit.
	plan, _, err := d.Registry.PlanFor(unattended.With(ctx), st.Action, req)
	var ae *actions.Error
	switch {
	case err == nil:
	case errors.Is(err, unattended.ErrWouldPrompt), errors.As(err, &ae) && ae.Status >= 500:
		// The machine cannot tell now (a service is down): the run checks again.
		return nil
	default:
		return fmt.Errorf("%s %s: %s", st.Action, st.Target, clean(err.Error()))
	}
	if why := actions.Refusal(plan); why != "" {
		return fmt.Errorf("%s %s: %s", st.Action, st.Target, why)
	}
	return nil
}

func (d CheckDeps) protect(action, target string) string {
	if d.Protect == nil {
		return ""
	}
	return d.Protect(action, target)
}
