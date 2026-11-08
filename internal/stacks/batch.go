package stacks

import (
	"context"
	"errors"
	"fmt"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// ErrUnauthorized means polkit refused a batch, or would have had to ask.
var ErrUnauthorized = broker.ErrUnauthorized

// Batch runs several stacks' start, stop or restart with one polkit check
// (stack.batch, ROADMAP 10.4). With interactive false no dialog can appear:
// polkit answers from what is already allowed, or refuses. A refusal of the
// whole batch is an error; a stack that failed on its own is a result.
func Batch(ctx context.Context, b Broker, items []broker.StackBatchItem, interactive bool) ([]broker.StackBatchResult, error) {
	if len(items) == 0 {
		return []broker.StackBatchResult{}, nil
	}
	var reply broker.StackBatchReply
	if err := b.Call(ctx, "stack.batch", broker.StackBatchParams{Items: items, Interactive: interactive}, &reply); err != nil {
		return nil, err
	}
	if len(reply.Results) != len(items) {
		return nil, errors.New("stacks: the broker answered for a different batch")
	}
	for i, r := range reply.Results {
		if r.Name != items[i].Name || r.Verb != items[i].Verb {
			return nil, errors.New("stacks: the broker answered for a different batch")
		}
	}
	return reply.Results, nil
}

// State is a managed stack's state: running, partial, stopped or absent.
func State(ctx context.Context, b Broker, name string) (string, error) {
	if !spec.ValidName(name) {
		return "", fmt.Errorf("stacks: %q is not a stack name", name)
	}
	var list []broker.StackInfo
	if err := b.Call(ctx, "stack.status", map[string]string{"name": name}, &list); err != nil {
		return "", err
	}
	if len(list) != 1 {
		return "", errors.New("stacks: unexpected status")
	}
	return list[0].State, nil
}
