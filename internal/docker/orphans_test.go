package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// orphanBroker lists one unused network and two unused volumes.
type orphanBroker struct {
	calls  []string
	params []string
	err    error
}

func (o *orphanBroker) Call(_ context.Context, method string, params, out any) error {
	o.calls = append(o.calls, method)
	if strings.HasSuffix(method, ".remove") {
		b, _ := json.Marshal(params)
		o.params = append(o.params, string(b))
		return o.err
	}
	if method == "docker.prune.preview" {
		pv := broker.PrunePreview{Items: []broker.PruneItem{{ID: "net1", Name: "old_default"}}}
		if params.(map[string]string)["kind"] == "volumes" {
			pv.Items = []broker.PruneItem{{ID: "data", Name: "data", Size: 2048}, {ID: strings.Repeat("a", 64), Name: strings.Repeat("a", 64), Size: -1}}
		}
		*out.(*broker.PrunePreview) = pv
	}
	return nil
}

func (o *orphanBroker) Stream(context.Context, string, any, func(json.RawMessage) error) error {
	return nil
}

func TestPlanOrphans(t *testing.T) {
	b := &orphanBroker{}
	ctx := context.Background()
	k := orphanKinds[1]
	p, err := planOrphan(ctx, b, k, "data")
	if err != nil || len(p.Items) != 1 || p.Confirm != "data" || p.Items[0].Detail != "2.0 KiB" {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = planOrphan(ctx, b, k, strings.Repeat("a", 64))
	if p.Confirm != strings.Repeat("a", 12) || p.Items[0].Detail != "" {
		t.Fatalf("%+v", p)
	}
	// In use, or not there: nothing to remove, and the broker is not asked.
	p, _ = planOrphan(ctx, b, k, "busy")
	if len(p.Items) != 0 || p.Blocked[0].Reason != ReasonInUse {
		t.Fatalf("%+v", p)
	}
	res, err := runOrphan(ctx, b, k, p)
	if err != nil || res.Done != 0 || len(b.params) != 0 {
		t.Fatalf("%+v %v %v", res, err, b.params)
	}
	for _, n := range []string{"bridge", "host", "none"} {
		if p, _ := planOrphan(ctx, b, orphanKinds[0], n); len(p.Items) != 0 {
			t.Fatalf("%s: %+v", n, p)
		}
	}
	p, _ = planOrphan(ctx, b, orphanKinds[0], "net1")
	if len(p.Items) != 1 || p.Confirm != "" {
		t.Fatalf("%+v", p)
	}
}

func TestRunOrphanNeverForces(t *testing.T) {
	b := &orphanBroker{}
	ctx := context.Background()
	for _, k := range orphanKinds {
		target := map[string]string{"network": "net1", "volume": "data"}[k.kind]
		p, _ := planOrphan(ctx, b, k, target)
		if res, err := runOrphan(ctx, b, k, p); err != nil || res.Done != 1 {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if b.params[0] != `{"id":"net1"}` || b.params[1] != `{"name":"data"}` {
		t.Fatalf("%v", b.params)
	}
}

// A container that took the volume after the preview: the Engine's 409
// reaches the user as a failure.
func TestRunOrphanRace(t *testing.T) {
	b := &orphanBroker{err: broker.Errorf(broker.CodeConflict, "docker: volume is in use")}
	k := orphanKinds[1]
	p, _ := planOrphan(context.Background(), b, k, "data")
	res, err := runOrphan(context.Background(), b, k, p)
	if err != nil || res.Done != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Detail, "in use") {
		t.Fatalf("%+v %v", res, err)
	}
	b.err = errors.New("what")
	if _, err := runOrphan(context.Background(), b, k, p); err == nil {
		t.Fatal("an unknown failure read as done")
	}
}

func TestOrphanValidate(t *testing.T) {
	reg := actions.NewRegistry(nil)
	RegisterOrphans(reg, &orphanBroker{})
	for _, k := range orphanKinds {
		for _, bad := range []string{"", "../x", "a/b", "a b", "-f", "a\nb"} {
			if k.valid(bad) {
				t.Errorf("%s accepted %q", k.kind, bad)
			}
		}
	}
}
