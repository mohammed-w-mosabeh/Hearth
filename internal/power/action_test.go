package power

import (
	"context"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type noAudit struct{}

func (noAudit) Audit(context.Context, store.AuditEntry) error { return nil }

func TestProfileAction(t *testing.T) {
	bus := newFakeBus()
	w := start(t, bus)
	reg := actions.NewRegistry(noAudit{})
	w.Register(reg)
	a, ok := reg.Lookup("power/profile")
	if !ok {
		t.Fatal("not registered")
	}
	for _, bad := range []actions.Request{
		{Target: "turbo", Params: map[string]string{}},
		{Target: "balanced", Params: map[string]string{"x": "1"}},
	} {
		if a.Validate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	r := actions.Request{Target: "power-saver", Params: map[string]string{}}
	p, err := a.Plan(context.Background(), r)
	if err != nil || len(p.Items) != 1 || len(p.Blocked) != 0 {
		t.Fatalf("%v %+v", err, p)
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if got := w.Snapshot().Profile.Active; got != "power-saver" {
		t.Fatal(got)
	}
}
