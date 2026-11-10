package posture

import (
	"context"
	"errors"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

// RegisterActions adds debsecan/check (ROADMAP 12.5): debsecan runs now and
// its list is kept, as the page's button does. It is safe and never asks
// for a password; an automation the user created counts as the user
// asking (SECURITY S21c).
func (s *Scanner) RegisterActions(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "debsecan", Name: "check", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			if r.Target != "debsecan" || len(r.Params) != 0 {
				return errors.New(`target must be "debsecan" and params empty`)
			}
			return nil
		},
		Plan: func(context.Context, actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "list known CVEs with debsecan (fetches the Debian security tracker's public data)",
				Items: []actions.Item{{ID: "debsecan", Name: "debsecan"}}, Blocked: []actions.Blocked{}}, nil
		},
		Run: func(ctx context.Context, _ actions.Request, _ actions.Plan) (actions.Result, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			v, err := s.RunCVEs(ctx)
			fail := func(detail string) (actions.Result, error) {
				return actions.Result{Failed: []actions.Blocked{{Item: actions.Item{ID: "debsecan", Name: "debsecan", Detail: detail}, Reason: "error"}}}, nil
			}
			switch {
			case errors.Is(err, ErrBusy):
				return fail("debsecan is already running")
			case err != nil:
				return fail("debsecan could not run")
			case !v.Installed:
				return fail("debsecan is not installed")
			case v.Error != "":
				return fail(v.Error)
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{},
				Blocked: []actions.Blocked{}}, nil
		}})
}
