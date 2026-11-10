package reports

import (
	"context"
	"errors"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

// RegisterActions adds report/generate (ROADMAP 12.5): a machine report
// for the day, week or month, saved beside the weekly ones and listed on
// the Reports page. Safe, local, no network, never asks for a password.
func (w *Weekly) RegisterActions(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "report", Name: "generate", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			if _, ok := Periods[r.Target]; !ok || len(r.Params) != 0 {
				return errors.New(`target must be "day", "week" or "month" and params empty`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "save a machine report for the past " + r.Target,
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}, Blocked: []actions.Blocked{}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
			_, err := w.Generate(ctx, r.Target)
			if err != nil {
				return actions.Result{Failed: []actions.Blocked{{Item: actions.Item{ID: r.Target, Name: r.Target, Detail: "the report could not be saved"}, Reason: "error"}}}, nil
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}})
}
