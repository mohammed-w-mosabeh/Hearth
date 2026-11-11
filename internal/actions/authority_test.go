package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
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

func TestRunUnattended(t *testing.T) {
	au := &memAudit{}
	reg := NewRegistry(au)
	ran := map[string]bool{}
	mk := func(kind, name string, risk Risk, auth Authority, plan Plan) {
		a := stub(Action{Kind: kind, Name: name, Risk: risk, Authority: auth})
		a.Plan = func(context.Context, Request) (Plan, error) { return plan, nil }
		a.Run = func(ctx context.Context, _ Request, _ Plan) (Result, error) {
			if !unattended.Is(ctx) {
				t.Errorf("%s/%s ran without the unattended mark", kind, name)
			}
			ran[kind+"/"+name] = true
			return Result{Done: 1}, nil
		}
		reg.Register(a)
	}
	mk("a", "safe", Safe, AuthorityNone, Plan{})
	mk("a", "polkit", Safe, AuthorityPolkit, Plan{})
	mk("a", "destructive", Destructive, AuthorityNone, Plan{})
	mk("a", "raised", Reversible, AuthorityNone, Plan{Risk: Destructive})
	mk("a", "confirm", Reversible, AuthorityNone, Plan{Confirm: "x"})
	if _, err := reg.RunUnattended(context.Background(), "a/safe", Request{Target: "t"}, "automation:1"); err != nil || !ran["a/safe"] {
		t.Fatal(err)
	}
	if len(au.entries) != 1 || au.entries[0].Origin != "automation:1" || au.entries[0].Action != "a.safe" {
		t.Fatalf("%+v", au.entries)
	}
	for _, k := range []string{"a/polkit", "a/destructive", "a/raised", "a/confirm"} {
		if _, err := reg.RunUnattended(context.Background(), k, Request{Target: "t"}, "automation:1"); !errors.Is(err, ErrNeedsApproval) || ran[k] {
			t.Errorf("%s: %v ran=%v", k, err, ran[k])
		}
	}
	if _, err := reg.RunUnattended(context.Background(), "a/none", Request{}, "x"); err == nil {
		t.Error("ran an unknown action")
	}
}
