package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// batchBroker answers stack.batch with reply.
type batchBroker struct {
	reply  broker.StackBatchReply
	err    error
	params []string
}

func (b *batchBroker) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	b.params = append(b.params, method+" "+string(raw))
	if b.err != nil {
		return b.err
	}
	return remarshal(b.reply, out)
}

func TestBatch(t *testing.T) {
	items := []broker.StackBatchItem{{Name: "photos", Verb: "stop"}, {Name: "notes", Verb: "start"}}
	b := &batchBroker{reply: broker.StackBatchReply{Results: []broker.StackBatchResult{
		{Name: "photos", Verb: "stop", OK: true}, {Name: "notes", Verb: "start", Error: "docker: failed"},
	}}}
	res, err := Batch(context.Background(), b, items, true)
	if err != nil || len(res) != 2 || !res[0].OK || res[1].OK {
		t.Fatalf("%v %+v", err, res)
	}
	want := `stack.batch {"items":[{"name":"photos","verb":"stop"},{"name":"notes","verb":"start"}],"interactive":true}`
	if len(b.params) != 1 || b.params[0] != want {
		t.Fatalf("%v", b.params)
	}
	// An answer for other stacks is not believed.
	b.reply.Results[1].Name = "other"
	if _, err := Batch(context.Background(), b, items, false); err == nil {
		t.Fatal("accepted another batch's answer")
	}
	b.reply.Results = b.reply.Results[:1]
	if _, err := Batch(context.Background(), b, items, false); err == nil {
		t.Fatal("accepted a short answer")
	}
	b.err = broker.ErrUnauthorized
	if _, err := Batch(context.Background(), b, items, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	// Nothing to do asks nothing.
	b.params = nil
	if res, err := Batch(context.Background(), b, nil, true); err != nil || len(res) != 0 || len(b.params) != 0 {
		t.Fatal(res, err, b.params)
	}
}

func TestState(t *testing.T) {
	f := &fakeBroker{status: []broker.StackInfo{{Name: "photos", State: "partial"}}}
	if s, err := State(context.Background(), f, "photos"); err != nil || s != "partial" {
		t.Fatal(s, err)
	}
	if _, err := State(context.Background(), f, "ghost"); err == nil {
		t.Fatal("found a ghost")
	}
	if _, err := State(context.Background(), f, "../x"); err == nil || len(f.calls) != 2 {
		t.Fatal("asked about a bad name", err)
	}
}
