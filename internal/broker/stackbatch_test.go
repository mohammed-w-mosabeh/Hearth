package broker

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

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
	if m := r.methods["stack.batch"]; m.Action != ActionStackControl || m.Interactive == nil {
		t.Fatal("batch method")
	}
}

// quietAuthority counts how polkit was asked.
type quietAuthority struct{ interactive, quiet int }

func (q *quietAuthority) Check(context.Context, Caller, string) (bool, error) {
	q.interactive++
	return true, nil
}

func (q *quietAuthority) CheckQuiet(context.Context, Caller, string) (bool, error) {
	q.quiet++
	return true, nil
}

// interactiveOnly cannot ask without a dialog.
type interactiveOnly struct{ calls int }

func (a *interactiveOnly) Check(context.Context, Caller, string) (bool, error) {
	a.calls++
	return true, nil
}

// TestStackBatchQuiet: interactive false never asks polkit in a way that
// could show a dialog (SECURITY S15b); true asks once for the whole batch.
func TestStackBatchQuiet(t *testing.T) {
	r := newLifeRig(t)
	for _, s := range []string{lifeSpec, otherSpec} {
		r.engine.containers = nil // the fake answers every project with the last one's containers
		if _, err := r.call(t, "stack.deploy", deployParams(s)); err != nil {
			t.Fatal(err)
		}
	}
	methods := StackLifeMethods(StackConfig{Store: r.store, Paths: r.paths, Engine: r.engine, ProcRoot: r.proc,
		Run: func(context.Context, string) error { return nil }})
	items := `[{"name":"web","verb":"stop"},{"name":"notes","verb":"stop"}]`

	auth := &quietAuthority{}
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, methods...); c.Authority = auth })
	for _, line := range []string{
		`{"id":1,"method":"stack.batch","params":{"items":` + items + `}}`,
		`{"id":2,"method":"stack.batch","params":{"items":` + items + `,"interactive":false}}`,
	} {
		if resp := h.raw(line); len(resp) != 1 || resp[0].Error != nil {
			t.Fatalf("%s: %+v", line, resp)
		}
	}
	if auth.quiet != 2 || auth.interactive != 0 {
		t.Fatalf("quiet %d, interactive %d", auth.quiet, auth.interactive)
	}
	resp := h.raw(`{"id":3,"method":"stack.batch","params":{"items":` + items + `,"interactive":true}}`)
	if len(resp) != 1 || resp[0].Error != nil || auth.interactive != 1 {
		t.Fatalf("%+v %+v", resp, auth)
	}
	var reply StackBatchReply
	if err := json.Unmarshal(resp[0].Result, &reply); err != nil || len(reply.Results) != 2 {
		t.Fatalf("%v %s", err, resp[0].Result)
	}

	// An authority that cannot stay quiet refuses, and is never asked.
	only := &interactiveOnly{}
	h2 := start(t, true, func(c *Config) { c.Methods = append(c.Methods, methods...); c.Authority = only })
	resp = h2.raw(`{"id":1,"method":"stack.batch","params":{"items":` + items + `}}`)
	if len(resp) != 1 || resp[0].Error == nil || resp[0].Error.Code != CodeUnauthorized || only.calls != 0 {
		t.Fatalf("%+v calls %d", resp, only.calls)
	}
	if !strings.Contains(resp[0].Error.Message, "not authorized") {
		t.Fatal(resp[0].Error.Message)
	}
}
