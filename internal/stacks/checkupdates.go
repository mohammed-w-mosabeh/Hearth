package stacks

import (
	"context"
	"errors"
	"fmt"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// maxChecked bounds the apps one check-updates run asks registries for.
const maxChecked = 32

// registerCheckUpdates adds stack/check-updates (ROADMAP 12.5, plan Q4):
// asks the registries, as an update preview does, whether an app ("all"
// or one by name) has newer images, and keeps the note the Apps page and
// the image_update trigger read. It changes nothing on the machine: safe,
// and it never asks for a password (stack.status needs no polkit). An
// automation the user created counts as the user asking (S21a).
func registerCheckUpdates(reg *actions.Registry, b Broker, tags docker.TagChecker, notes *UpdateNotes) {
	reg.Register(actions.Action{Kind: Kind, Name: "check-updates", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			if (r.Target != "all" && !spec.ValidName(r.Target)) || len(r.Params) != 0 {
				return errors.New(`target must be "all" or a stack name, and params empty`)
			}
			return nil
		},
		// The preview asks no registry: it only says what a run does.
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "look for newer images of " + r.Target + " (asks their registries)",
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}, Blocked: []actions.Blocked{}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
			names := []string{r.Target}
			if r.Target == "all" {
				var list []broker.StackInfo
				if err := b.Call(ctx, "stack.status", nil, &list); err != nil {
					return res, brokerError(err)
				}
				names = names[:0]
				for _, s := range list {
					names = append(names, s.Name)
				}
			}
			for i, n := range names {
				if i >= maxChecked {
					res.Blocked = append(res.Blocked, actions.Blocked{Item: actions.Item{ID: n, Name: n, Detail: fmt.Sprintf("more than %d apps", maxChecked)}, Reason: ReasonRefused})
					continue
				}
				up, err := planUpdate(ctx, b, tags, notes, n)
				switch {
				case err != nil:
					res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: n, Name: n, Detail: "could not be checked"}, Reason: ReasonError})
				case len(up.Blocked) > 0:
					res.Blocked = append(res.Blocked, up.Blocked...)
				default:
					res.Done++
				}
			}
			return res, nil
		}})
}
