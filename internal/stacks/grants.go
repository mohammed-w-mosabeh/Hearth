package stacks

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

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

// GrantCache answers whether a live grant covers a stack and verb, for
// the actions' authority (SECURITY S15d): a covered step runs unattended,
// the broker checking the grant itself. It asks the broker at most every
// 30 seconds; an error counts as no grant.
type GrantCache struct {
	B   Broker
	Now func() time.Time

	mu sync.Mutex
	at time.Time
	m  map[string][]string
}

const grantCacheFor = 30 * time.Second

// Covers reports whether the user's grant lets verb run on name unattended.
func (g *GrantCache) Covers(name, verb string) bool {
	if g == nil || g.B == nil {
		return false
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil || now().Sub(g.at) >= grantCacheFor {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m, err := Grants(ctx, g.B)
		cancel()
		if err != nil {
			m = map[string][]string{}
		}
		g.m, g.at = m, now()
	}
	verbs := g.m[name]
	return slices.Contains(verbs, verb) && (verb != "restart" || slices.Contains(verbs, "start"))
}

// Forget drops what the cache holds (after a grant changes).
func (g *GrantCache) Forget() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.m = nil
	g.mu.Unlock()
}

// RegisterGrants adds stack/grant (target: a stack; params {"start":
// "true"} to allow start as well as stop, and {"restart": "true"} with it
// to allow restart) and stack/revoke-grant. Granting asks the owner's
// password once (org.hearth.stack.grant, no keep); the broker keeps and
// checks the grant for 90 days. cache, which may be nil, is forgotten
// after each change.
func RegisterGrants(reg *actions.Registry, b Broker, cache *GrantCache) {
	reg.Register(actions.Action{Kind: Kind, Name: "grant", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a stack name")
			}
			for k, v := range r.Params {
				if (k != "start" && k != "restart") || (v != "true" && v != "false") {
					return errors.New(`params are start and restart, "true" or "false"`)
				}
			}
			if r.Params["restart"] == "true" && r.Params["start"] != "true" {
				return errors.New("restart needs start as well")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			what := "stop"
			switch {
			case r.Params["restart"] == "true":
				what = "stop, start or restart"
			case r.Params["start"] == "true":
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
			defer cache.Forget()
			return call(ctx, b, p, "stack.grant.set", broker.StackGrantParams{Name: r.Target, Start: r.Params["start"] == "true",
				Restart: r.Params["restart"] == "true"})
		}})
	reg.Register(actions.Action{Kind: Kind, Name: "revoke-grant", Risk: actions.Safe, Validate: nameOnly,
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "stop letting modes change " + r.Target, Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			defer cache.Forget()
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
