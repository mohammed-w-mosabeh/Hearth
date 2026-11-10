package actions

import (
	"context"
	"testing"
)

func stub(a Action) Action {
	a.Validate = func(Request) error { return nil }
	a.Plan = func(context.Context, Request) (Plan, error) { return Plan{}, nil }
	a.Run = func(context.Context, Request, Plan) (Result, error) { return Result{}, nil }
	return a
}

func TestAuthority(t *testing.T) {
	reg := NewRegistry(nil)
	reg.Register(stub(Action{Kind: "unit", Name: "stop", Risk: Reversible}))
	reg.Register(stub(Action{Kind: "power", Name: "profile", Risk: Safe}))
	reg.Register(stub(Action{Kind: "container", Name: "restart", Risk: Reversible}))
	reg.Register(stub(Action{Kind: "x", Name: "declared", Risk: Safe, Authority: AuthorityNone}))
	reg.Register(stub(Action{Kind: "x", Name: "perreq", Risk: Safe, AuthorityFor: func(r Request) Authority {
		if r.Target == "safe" {
			return AuthorityNone
		}
		return AuthorityPolkit
	}}))
	for _, c := range []struct {
		key, target string
		want        Authority
	}{
		{"unit/stop", "user/syncnotes.service", AuthorityNone},
		{"unit/stop", "system/cups.service", AuthorityPolkit},
		{"unit/stop", "userx/cups.service", AuthorityPolkit},
		{"power/profile", "power-saver", AuthorityNone},
		{"container/restart", "web", AuthorityPolkit},
		{"x/declared", "", AuthorityNone},
		{"x/perreq", "safe", AuthorityNone},
		{"x/perreq", "other", AuthorityPolkit},
		{"not/registered", "", AuthorityPolkit},
	} {
		if got := reg.AuthorityOf(c.key, Request{Target: c.target}); got != c.want {
			t.Errorf("%s %s: %s", c.key, c.target, got)
		}
	}
}
