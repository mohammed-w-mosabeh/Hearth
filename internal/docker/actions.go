// Package docker is hearthd's side of the Docker pages (SPEC 5, ROADMAP
// 2.4): container actions, inspect, live logs and stats. hearthd has no
// access to the Docker socket (it is root); every request goes through
// hearth-broker's allowlisted methods, which ask polkit for changes.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Reasons an item is blocked or failed; the UI translates them.
const (
	ReasonUnauthorized = "unauthorized" // polkit refused or the dialog was cancelled
	ReasonNotFound     = "not-found"    // no such container
	ReasonError        = "error"        // anything else (Detail: the error)
)

// Broker is the subset of broker.Client the package needs.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
	Stream(ctx context.Context, method string, params any, fn func(json.RawMessage) error) error
}

type op struct {
	name string
	risk actions.Risk
}

var ops = []op{
	{name: "start", risk: actions.Safe},
	{name: "stop", risk: actions.Reversible},
	{name: "restart", risk: actions.Reversible},
	{name: "pause", risk: actions.Reversible},
	{name: "unpause", risk: actions.Safe},
	{name: "remove", risk: actions.Destructive},
}

// state is what a plan saw of the container.
type state struct {
	ID, Name, Status string
	Running          bool
}

// Register adds the container/* actions to reg. Removing a running
// container needs its name typed back (it is stopped by force).
func Register(reg *actions.Registry, b Broker) {
	for _, o := range ops {
		reg.Register(actions.Action{Kind: "container", Name: o.name, Risk: o.risk,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				if !broker.ValidContainer(r.Target) {
					return errors.New("target must be a container ID or name")
				}
				return nil
			},
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) { return plan(ctx, b, o, r.Target) },
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return run(ctx, b, o, p)
			}})
	}
}

func plan(ctx context.Context, b Broker, o op, target string) (actions.Plan, error) {
	st, err := inspectState(ctx, b, target)
	p := actions.Plan{Summary: o.name + " " + target, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if err != nil {
		if bl, ok := failure(actions.Item{ID: target, Name: target}, err); ok {
			p.Blocked = append(p.Blocked, bl)
			return p, nil
		}
		return p, brokerError(err)
	}
	p.Summary = o.name + " " + st.Name
	p.Items = append(p.Items, actions.Item{ID: st.ID, Name: st.Name, Detail: st.Status})
	p.State = st
	if o.name == "remove" && st.Running {
		p.Confirm = st.Name
	}
	return p, nil
}

func inspectState(ctx context.Context, b Broker, id string) (state, error) {
	var res struct {
		Inspect struct {
			ID    string `json:"Id"`
			Name  string
			State struct {
				Status  string
				Running bool
			}
		} `json:"inspect"`
	}
	if err := b.Call(ctx, "docker.containers.inspect", map[string]string{"id": id}, &res); err != nil {
		return state{}, err
	}
	in := res.Inspect
	return state{ID: in.ID, Name: strings.TrimPrefix(in.Name, "/"), Status: in.State.Status, Running: in.State.Running}, nil
}

func run(ctx context.Context, b Broker, o op, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
	st, ok := p.State.(state)
	if len(p.Items) == 0 || !ok {
		return res, nil
	}
	params := map[string]any{"id": st.ID}
	if o.name == "remove" {
		params["force"] = st.Running // what the user confirmed
	}
	if err := b.Call(ctx, "docker.containers."+o.name, params, nil); err != nil {
		bl, ok := failure(p.Items[0], err)
		if !ok {
			return res, brokerError(err)
		}
		res.Failed = append(res.Failed, bl)
		return res, nil
	}
	res.Done = 1
	return res, nil
}

// failure turns a broker error about the item into a reason; ok is false
// for errors about the broker itself.
func failure(it actions.Item, err error) (actions.Blocked, bool) {
	var be *broker.Error
	switch {
	case errors.Is(err, broker.ErrUnauthorized):
		return actions.Blocked{Item: it, Reason: ReasonUnauthorized}, true
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		return actions.Blocked{Item: it, Reason: ReasonNotFound}, true
	case errors.As(err, &be) && (be.Code == broker.CodeFailed || be.Code == broker.CodeConflict):
		it.Detail = strings.TrimPrefix(be.Message, "docker: ")
		return actions.Blocked{Item: it, Reason: ReasonError}, true
	}
	return actions.Blocked{}, false
}

// brokerError is an HTTP error for a broker that cannot serve at all.
func brokerError(err error) error {
	switch {
	case errors.Is(err, broker.ErrUnavailable):
		return actions.Errorf(http.StatusServiceUnavailable, "hearth-broker is not available")
	case errors.Is(err, broker.ErrNotMember):
		return actions.Errorf(http.StatusForbidden, "you are not in the hearth group")
	case errors.Is(err, broker.ErrNoDocker):
		return actions.Errorf(http.StatusServiceUnavailable, "docker is not available")
	}
	return actions.Errorf(http.StatusBadGateway, "broker: %v", err)
}
