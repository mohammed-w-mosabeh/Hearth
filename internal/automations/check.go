package automations

import (
	"context"
	"fmt"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
)

// NewChecker checks what a spec cannot know by itself: every action is in
// the registry (known), and a schedule means something to systemd
// (calendar). From 12.3 the engine adds the target checks.
func NewChecker(known func(action string) bool, calendar func(ctx context.Context, expr string) error) Checker {
	return func(ctx context.Context, s spec.Spec) error {
		var err error
		s.Steps(func(st spec.Step) {
			if err == nil && st.Step == "action" && !known(st.Action) {
				err = fmt.Errorf("%s is not an action Hearth has", st.Action)
			}
		})
		if err != nil {
			return err
		}
		if s.Trigger.Kind == "schedule" && calendar != nil {
			if err := calendar(ctx, s.Trigger.Calendar); err != nil {
				return fmt.Errorf("schedule: %w", err)
			}
		}
		return nil
	}
}
