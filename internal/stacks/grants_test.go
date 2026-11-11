package stacks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

func TestGrants(t *testing.T) {
	gb := &grantBroker{list: []broker.StackGrant{{Name: "photos", Verbs: []string{"stop"}}, {Name: "../x", Verbs: []string{"stop"}}}}
	got, err := Grants(context.Background(), gb)
	if err != nil || len(got) != 1 || got["photos"][0] != "stop" {
		t.Fatal(got, err)
	}
	reg := actions.NewRegistry(nil)
	RegisterGrants(reg, gb, nil)
	a, _ := reg.Lookup("stack/grant")
	for _, bad := range []actions.Request{
		{Target: "../x", Params: map[string]string{}},
		{Target: "photos", Params: map[string]string{"start": "yes"}},
		{Target: "photos", Params: map[string]string{"verbs": "restart"}},
	} {
		if a.Validate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if a.Validate(actions.Request{Target: "photos", Params: map[string]string{"start": "true"}}) != nil {
		t.Fatal("refused start")
	}
	if a.Risk == actions.Safe {
		t.Fatal("granting is not a safe action")
	}
}

type grantBroker struct{ list []broker.StackGrant }

func (g *grantBroker) Call(_ context.Context, method string, _, out any) error {
	if method == "stack.grant.list" {
		return remarshal(g.list, out)
	}
	return nil
}

type countingBroker struct {
	n     int
	verbs []string
	err   error
}

func (c *countingBroker) Call(_ context.Context, method string, _, out any) error {
	c.n++
	if c.err != nil {
		return c.err
	}
	return remarshal([]broker.StackGrant{{Name: "web", Verbs: c.verbs}}, out)
}

func TestGrantCache(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &countingBroker{verbs: []string{"stop", "start", "restart"}}
	g := &GrantCache{B: b, Now: func() time.Time { return now }}
	if !g.Covers("web", "restart") || !g.Covers("web", "stop") || g.Covers("db", "stop") || g.Covers("web", "remove") {
		t.Fatal("covers")
	}
	if b.n != 1 {
		t.Fatalf("asked %d times within the cache's time", b.n)
	}
	b.verbs = []string{"stop", "restart"} // restart without start never counts
	now = now.Add(grantCacheFor)
	if g.Covers("web", "restart") || !g.Covers("web", "stop") || b.n != 2 {
		t.Fatal("refresh")
	}
	b.err = errors.New("broker down")
	g.Forget()
	if g.Covers("web", "stop") {
		t.Fatal("an error counts as no grant")
	}
	var none *GrantCache
	if none.Covers("web", "stop") {
		t.Fatal("nil cache")
	}
	reg := actions.NewRegistry(nopAudit{})
	b.err, b.verbs = nil, []string{"stop", "start", "restart"}
	g.Forget()
	Register(reg, b, fakeTags{}, nil, g)
	if reg.AuthorityOf("stack/restart", actions.Request{Target: "web"}) != actions.AuthorityNone ||
		reg.AuthorityOf("stack/restart", actions.Request{Target: "db"}) != actions.AuthorityPolkit {
		t.Fatal("a granted stack verb never asks; others may")
	}
}
