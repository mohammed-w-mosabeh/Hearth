package stacks

import (
	"context"
	"errors"
	"net/http"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Grants are the user's live unattended grants (ROADMAP 10.8): stack name
// to the verbs a trigger may run on it with nobody there.
func Grants(ctx context.Context, b Broker) (map[string][]string, error) {
	var list []broker.StackGrant
	if err := b.Call(ctx, "stack.grant.list", nil, &list); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, g := range list {
		if spec.ValidName(g.Name) {
			out[g.Name] = g.Verbs
		}
	}
	return out, nil
}

// RegisterGrants adds stack/grant (target: a stack; params {"start":
// "true"} to allow start as well as stop) and stack/revoke-grant. Granting
// asks the owner's password once (org.hearth.stack.grant, no keep); the
// broker keeps and checks the grant for 90 days.
func RegisterGrants(reg *actions.Registry, b Broker) {
	reg.Register(actions.Action{Kind: Kind, Name: "grant", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a stack name")
			}
			for k, v := range r.Params {
				if k != "start" || (v != "true" && v != "false") {
					return errors.New(`the only param is start, "true" or "false"`)
				}
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			what := "stop"
			if r.Params["start"] == "true" {
				what = "stop or start"
			}
			info, p, err := find(ctx, b, r.Target, "let modes "+what)
			if err != nil || len(p.Blocked) > 0 {
				return p, err
			}
			p.Items = []actions.Item{{ID: info.Name, Name: info.Name, Detail: what}}
			return p, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			if len(p.Blocked) > 0 {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.grant.set", broker.StackGrantParams{Name: r.Target, Start: r.Params["start"] == "true"})
		}})
	reg.Register(actions.Action{Kind: Kind, Name: "revoke-grant", Risk: actions.Safe, Validate: nameOnly,
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "stop letting modes change " + r.Target, Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return call(ctx, b, p, "stack.grant.revoke", broker.StackGrantName{Name: r.Target})
		}})
}

// HandleGrants serves GET /api/v1/stack-grants: the user's live grants (not
// under /stacks/, where a stack may be named "grants").
func HandleGrants(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var list []broker.StackGrant
		if err := b.Call(r.Context(), "stack.grant.list", nil, &list); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, list)
	}
}
