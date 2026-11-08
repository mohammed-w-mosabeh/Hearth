package broker

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// MaxStackBatch is how many stacks one stack.batch call may hold.
const MaxStackBatch = 32

// StackBatchItem is one stack and what to do to it.
type StackBatchItem struct {
	Name string `json:"name"`
	Verb string `json:"verb"` // start, stop or restart
}

// StackBatchParams is stack.batch's params (ROADMAP 10.4): a mode's stack
// actions, one polkit check for all of them. Interactive false asks polkit
// without allowing a dialog (SECURITY S15b), and is the default.
type StackBatchParams struct {
	Items       []StackBatchItem `json:"items"`
	Interactive bool             `json:"interactive"`
}

// StackBatchResult is what happened to one item.
type StackBatchResult struct {
	Name  string `json:"name"`
	Verb  string `json:"verb"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// StackBatchReply is stack.batch's result, one entry per item in order.
type StackBatchReply struct {
	Results []StackBatchResult `json:"results"`
}

// checkBatch validates every item as stack.control would, before polkit and
// again after it: an item that would be refused refuses the whole batch, so
// no dialog asks about it and nothing runs. raws holds each stored spec.
func (s *stacks) checkBatch(ctx context.Context, p json.RawMessage, pre bool) (StackBatchParams, [][]byte, error) {
	var v StackBatchParams
	if err := decodeParams(p, &v); err != nil {
		return v, nil, err
	}
	if len(v.Items) == 0 || len(v.Items) > MaxStackBatch {
		return v, nil, badParam("a batch holds 1 to %d stacks", MaxStackBatch)
	}
	seen := map[string]bool{}
	raws := make([][]byte, len(v.Items))
	for i, it := range v.Items {
		if _, ok := controlVerbs[it.Verb]; !ok {
			return v, nil, badParam("verb must be start, stop or restart")
		}
		if seen[it.Name] {
			return v, nil, badParam("stack %s is listed twice", it.Name)
		}
		seen[it.Name] = true
		_, raw, _, err := s.stored(it.Name)
		if err != nil {
			return v, nil, err
		}
		if pre && it.Verb != "stop" {
			if _, err := s.paths(ctx, raw, true); err != nil {
				return v, nil, err
			}
		}
		raws[i] = raw
	}
	return v, raws, nil
}

// batch runs the items in order after the one polkit check. A failed item
// does not stop the others; each result says how it went.
func (s *stacks) batch(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, raws, err := s.checkBatch(ctx, p, false)
	if err != nil {
		return nil, err
	}
	out := StackBatchReply{Results: make([]StackBatchResult, 0, len(v.Items))}
	for i, it := range v.Items {
		err := s.batchOne(ctx, it, raws[i])
		r := StackBatchResult{Name: it.Name, Verb: it.Verb, OK: err == nil}
		if err != nil {
			var be *Error
			if errors.As(err, &be) {
				r.Error = be.Message
			} else {
				r.Error = "failed"
			}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

func (s *stacks) batchOne(ctx context.Context, it StackBatchItem, raw []byte) error {
	defer s.locks.lock(it.Name)()
	verb := controlVerbs[it.Verb]
	if it.Verb == "stop" {
		_, err := s.run(ctx, it.Name, verb)
		return err
	}
	paths, err := s.paths(ctx, raw, false)
	if err != nil {
		return err
	}
	_, err = s.runAndVerify(ctx, it.Name, verb, paths)
	return err
}

// batchMethod is stack.batch: org.hearth.stack.control checked once.
func (s *stacks) batchMethod() Method {
	return Method{Name: "stack.batch", Privileged: true, Action: ActionStackControl, Timeout: 30 * time.Minute,
		Check: func(ctx context.Context, p json.RawMessage) error {
			_, _, err := s.checkBatch(ctx, p, true)
			return pre(err)
		},
		Interactive: func(p json.RawMessage) bool {
			var v StackBatchParams
			return decodeParams(p, &v) == nil && v.Interactive
		},
		Handle: s.batch}
}
