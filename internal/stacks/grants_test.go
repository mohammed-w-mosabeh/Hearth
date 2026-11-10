package stacks

import (
	"context"
	"testing"

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
	RegisterGrants(reg, gb)
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
