package docker

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// pruneBroker answers previews by kind and records prunes.
type pruneBroker struct {
	fakeBroker
	previews map[string]broker.PrunePreview
	pruned   []string
	result   broker.PruneResult
}

func (p *pruneBroker) Call(ctx context.Context, method string, params, out any) error {
	b, _ := json.Marshal(params)
	switch method {
	case "docker.prune.preview":
		var v struct{ Kind string }
		_ = json.Unmarshal(b, &v)
		pv, ok := p.previews[v.Kind]
		if !ok {
			return broker.Errorf(broker.CodeFailed, "docker: boom")
		}
		raw, _ := json.Marshal(pv)
		return json.Unmarshal(raw, out)
	case "docker.prune":
		p.pruned = append(p.pruned, string(b))
		raw, _ := json.Marshal(p.result)
		return json.Unmarshal(raw, out)
	}
	return p.fakeBroker.Call(ctx, method, params, out)
}

func TestCleanupSummary(t *testing.T) {
	p := &pruneBroker{previews: map[string]broker.PrunePreview{
		"containers": {Kind: "containers", Until: 100, Items: []broker.PruneItem{{ID: "a", Name: "old", Size: 5}, {ID: "b", Name: "older", Size: 50}}, Reclaimable: 55},
		"images":     {Kind: "images", Until: 100, Items: []broker.PruneItem{}},
		"networks":   {Kind: "networks", Until: 100, Items: []broker.PruneItem{{ID: "n", Name: "x_default", Size: 0}}},
		"volumes":    {Kind: "volumes", Until: 100, Items: []broker.PruneItem{{ID: "v", Name: "v", Size: -1}}},
	}}
	rec := httptest.NewRecorder()
	HandleCleanup(p)(rec, httptest.NewRequest("GET", "/x", nil))
	var got struct{ Kinds []KindSummary }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Kinds) != 5 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	c := got.Kinds[0]
	if c.Kind != "containers" || c.Count != 2 || c.Reclaimable != 55 || c.Items[0].Name != "older" {
		t.Fatalf("containers %+v", c)
	}
	if b := got.Kinds[4]; b.Kind != "build" || b.Error != "boom" {
		t.Fatalf("a failing kind is reported, not fatal: %+v", b)
	}

	p.previews = nil
	p.fakeBroker = fakeBroker{}
	rec = httptest.NewRecorder()
	HandleCleanup(&errBroker{err: broker.ErrNoDocker})(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 503 {
		t.Fatalf("no docker %d", rec.Code)
	}
}

type errBroker struct {
	fakeBroker
	err error
}

func (e *errBroker) Call(context.Context, string, any, any) error { return e.err }

func TestPruneActions(t *testing.T) {
	p := &pruneBroker{previews: map[string]broker.PrunePreview{
		"containers": {Kind: "containers", Until: 1700, Items: []broker.PruneItem{{ID: "a", Name: "old", Size: 2048}}},
		"volumes":    {Kind: "volumes", Until: 1800, Items: []broker.PruneItem{{ID: "db", Name: "db", Size: 1 << 30}, {ID: "cache", Name: "cache", Size: -1}}},
		"images":     {Kind: "images", Until: 1900, Items: []broker.PruneItem{}},
	}, result: broker.PruneResult{Deleted: []string{"a"}, Skipped: []string{"cache"}}}
	reg := actions.NewRegistry(nopAudit{})
	RegisterPrune(reg, p)
	post := func(kind, body string) (int, string) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("kind", "prune")
		req.SetPathValue("action", kind)
		rec := httptest.NewRecorder()
		reg.Handle(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, _ := post("containers", `{"target":"docker","params":{}}`); code == 200 {
		t.Fatal("pruned without a preview")
	}
	if len(p.pruned) != 0 {
		t.Fatal("reached the broker without a preview")
	}
	_, body := post("containers", `{"target":"docker","params":{},"preview":true}`)
	var plan struct {
		Token, Confirm string
		Items          []actions.Item
	}
	_ = json.Unmarshal([]byte(body), &plan)
	if plan.Token == "" || plan.Confirm != "" || plan.Items[0].Detail != "2.0 KiB" {
		t.Fatalf("plan %s", body)
	}
	if code, body := post("containers", `{"target":"docker","params":{},"token":"`+plan.Token+`"}`); code != 200 || !strings.Contains(body, `"done":1`) {
		t.Fatalf("run %d %s", code, body)
	}
	if p.pruned[0] != `{"kind":"containers","until":1700}` {
		t.Fatalf("prune %v", p.pruned)
	}

	// Volumes: the names must be typed, and exactly the previewed list goes.
	_, body = post("volumes", `{"target":"docker","params":{},"preview":true}`)
	_ = json.Unmarshal([]byte(body), &plan)
	if plan.Confirm != "delete 2 volumes" {
		t.Fatalf("volumes plan %s", body)
	}
	if code, _ := post("volumes", `{"target":"docker","params":{},"token":"`+plan.Token+`","confirm":"yes"}`); code == 200 {
		t.Fatal("volumes pruned without the typed confirmation")
	}
	_, body = post("volumes", `{"target":"docker","params":{},"preview":true}`)
	_ = json.Unmarshal([]byte(body), &plan)
	_, body = post("volumes", `{"target":"docker","params":{},"token":"`+plan.Token+`","confirm":"delete 2 volumes"}`)
	if p.pruned[len(p.pruned)-1] != `{"kind":"volumes","until":1800,"volumes":["db","cache"]}` || !strings.Contains(body, `"reason":"in-use"`) {
		t.Fatalf("volumes %v %s", p.pruned, body)
	}

	// Nothing to remove: the run does nothing.
	n := len(p.pruned)
	_, body = post("images", `{"target":"docker","params":{},"preview":true}`)
	_ = json.Unmarshal([]byte(body), &plan)
	_, _ = post("images", `{"target":"docker","params":{},"token":"`+plan.Token+`"}`)
	if len(p.pruned) != n {
		t.Fatal("empty prune reached the broker")
	}
	if code, _ := post("containers", `{"target":"other","params":{},"preview":true}`); code != 400 {
		t.Fatalf("bad target %d", code)
	}
}
