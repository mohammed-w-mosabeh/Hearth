package broker

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
)

const otherSpec = `{"version":1,"name":"notes","services":{"app":{"image":"nginx:1.27"}}}`

func TestStackBatch(t *testing.T) {
	r := newLifeRig(t)
	for _, s := range []string{lifeSpec, otherSpec} {
		r.engine.containers = nil // the fake answers every project with the last one's containers
		if _, err := r.call(t, "stack.deploy", deployParams(s)); err != nil {
			t.Fatal(err)
		}
	}
	r.ran, r.paths.checks = nil, 0
	res, err := r.call(t, "stack.batch", map[string]any{"items": []map[string]string{
		{"name": "web", "verb": "stop"}, {"name": "notes", "verb": "start"},
	}, "interactive": true})
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := res.(StackBatchReply)
	if !ok || len(reply.Results) != 2 || !reply.Results[0].OK || !reply.Results[1].OK || reply.Results[1].Name != "notes" {
		t.Fatalf("%+v", res)
	}
	// In order; start checks its binds before polkit, stop does not.
	if !slices.Equal(r.ran, []string{"stop", "up"}) || r.paths.checks != 1 {
		t.Fatalf("ran %v, path checks %d", r.ran, r.paths.checks)
	}
}

// One bad item refuses the whole batch before polkit: nothing runs.
func TestStackBatchRefusals(t *testing.T) {
	r := newLifeRig(t)
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	r.ran = nil
	many := make([]map[string]string, MaxStackBatch+1)
	for i := range many {
		many[i] = map[string]string{"name": "web", "verb": "stop"}
	}
	for name, p := range map[string]any{
		"empty":       map[string]any{"items": []any{}},
		"no items":    map[string]any{"interactive": true},
		"too many":    map[string]any{"items": many},
		"twice":       map[string]any{"items": []map[string]string{{"name": "web", "verb": "stop"}, {"name": "web", "verb": "start"}}},
		"bad verb":    map[string]any{"items": []map[string]string{{"name": "web", "verb": "down-volumes"}}},
		"missing":     map[string]any{"items": []map[string]string{{"name": "web", "verb": "stop"}, {"name": "ghost", "verb": "stop"}}},
		"bad name":    map[string]any{"items": []map[string]string{{"name": "../x", "verb": "stop"}}},
		"extra field": map[string]any{"items": []map[string]any{{"name": "web", "verb": "stop", "args": []string{"--privileged"}}}},
		"extra top":   map[string]any{"items": []map[string]string{{"name": "web", "verb": "stop"}}, "as_root": true},
		"bad bool":    map[string]any{"items": []map[string]string{{"name": "web", "verb": "stop"}}, "interactive": "yes"},
	} {
		if _, err := r.call(t, "stack.batch", p); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A bind that resolves badly refuses a start before polkit.
	r.paths.first = pathcheck.Reply{Refused: []string{"app: bind source /srv/site: it resolves to /etc"}}
	if _, err := r.call(t, "stack.batch", map[string]any{"items": []map[string]string{{"name": "web", "verb": "start"}}}); errCode(err) != CodeBadParams {
		t.Fatalf("bind: %v", err)
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v", r.ran)
	}
	if m := r.methods["stack.batch"]; m.Action != ActionStackControl || m.Authorized == nil {
		t.Fatal("batch method")
	}
}

// countingAuthority counts how often polkit was asked, and always allows.
type countingAuthority struct{ interactive int }

func (q *countingAuthority) Check(context.Context, Caller, string) (bool, error) {
	q.interactive++
	return true, nil
}

// TestStackBatchUnattended: interactive false never asks polkit at all, so
// no dialog can appear and no password typed earlier counts; only the
// owner's grants allow it, for every item (SECURITY S15b). Interactive
// true asks once for the whole batch.
func TestStackBatchUnattended(t *testing.T) {
	r := newLifeRig(t)
	for _, s := range []string{lifeSpec, otherSpec} {
		r.engine.containers = nil
		if _, err := r.call(t, "stack.deploy", deployParams(s)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.UnixMilli(1793359297763)
	methods := StackLifeMethods(StackConfig{Store: r.store, Paths: r.paths, Engine: r.engine, ProcRoot: r.proc, Now: func() time.Time { return now },
		Run: func(context.Context, string) error { return nil }})
	auth := &countingAuthority{}
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, methods...); c.Authority = auth })
	stops := `[{"name":"web","verb":"stop"},{"name":"notes","verb":"stop"}]`
	call := func(line string) []Response { t.Helper(); return h.raw(line) }
	refused := func(line string) {
		t.Helper()
		if resp := call(line); len(resp) != 1 || resp[0].Error == nil || resp[0].Error.Code != CodeUnauthorized {
			t.Fatalf("%s: %+v", line, resp)
		}
	}
	// No grants: refused, and polkit never asked.
	refused(`{"id":1,"method":"stack.batch","params":{"items":` + stops + `}}`)
	refused(`{"id":2,"method":"stack.batch","params":{"items":` + stops + `,"interactive":false}}`)
	if auth.interactive != 0 {
		t.Fatalf("%+v", auth)
	}
	// A grant for web only: a batch that also stops notes is refused.
	if resp := call(`{"id":3,"method":"stack.grant.set","params":{"name":"web"}}`); len(resp) != 1 || resp[0].Error != nil || auth.interactive != 1 {
		t.Fatalf("%+v %+v", resp, auth)
	}
	refused(`{"id":4,"method":"stack.batch","params":{"items":` + stops + `}}`)
	if resp := call(`{"id":5,"method":"stack.batch","params":{"items":[{"name":"web","verb":"stop"}]}}`); len(resp) != 1 || resp[0].Error != nil {
		t.Fatalf("%+v", resp)
	}
	// Stop only, unless start was chosen; restart never.
	refused(`{"id":6,"method":"stack.batch","params":{"items":[{"name":"web","verb":"start"}]}}`)
	refused(`{"id":7,"method":"stack.batch","params":{"items":[{"name":"web","verb":"restart"}]}}`)
	if resp := call(`{"id":8,"method":"stack.grant.set","params":{"name":"web","start":true}}`); resp[0].Error != nil {
		t.Fatalf("%+v", resp)
	}
	if resp := call(`{"id":9,"method":"stack.batch","params":{"items":[{"name":"web","verb":"start"}]}}`); resp[0].Error != nil {
		t.Fatalf("%+v", resp)
	}
	// After 90 days it lapses.
	now = now.Add(GrantTTL)
	refused(`{"id":10,"method":"stack.batch","params":{"items":[{"name":"web","verb":"stop"}]}}`)
	if auth.interactive != 2 {
		t.Fatalf("polkit was asked for an unattended batch: %+v", auth)
	}
	// Interactive asks once for the whole batch, grants or not.
	if resp := call(`{"id":11,"method":"stack.batch","params":{"items":` + stops + `,"interactive":true}}`); resp[0].Error != nil || auth.interactive != 3 {
		t.Fatalf("%+v %+v", resp, auth)
	}
	var reply StackBatchReply
	resp := call(`{"id":12,"method":"stack.batch","params":{"items":` + stops + `,"interactive":true}}`)
	if err := json.Unmarshal(resp[0].Result, &reply); err != nil || len(reply.Results) != 2 {
		t.Fatalf("%v %s", err, resp[0].Result)
	}
}
