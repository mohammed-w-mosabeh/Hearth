package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Reasons in an import's blocked and failed lists.
const (
	ReasonRefused      = "refused"
	ReasonNotFound     = "not-found"
	ReasonUnauthorized = "unauthorized"
	ReasonError        = "error"
	ReasonTooLarge     = "too-large"
)

type importState struct {
	params broker.StackImportParams
}

// RegisterAction adds stack/import (ROADMAP 9.8): its preview is the
// dry-run, it needs the project's name typed back, and it runs one
// stack.import, which asks polkit. The secret values live only in the
// preview's state, for the token's two minutes, and go to the broker
// alone; the request params (plain and skip lists) are all the audit sees.
func RegisterAction(reg *actions.Registry, d Deps) {
	reg.Register(actions.Action{Kind: "stack", Name: "import", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a Compose project that can be a stack name")
			}
			for k, v := range r.Params {
				if k != "plain" && k != "skip" {
					return fmt.Errorf("unknown param %q", k)
				}
				if len(v) > 8<<10 {
					return fmt.Errorf("%s is too long", k)
				}
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planImport(ctx, d, r)
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
			st, ok := p.State.(importState)
			if !ok {
				return res, nil
			}
			it := actions.Item{ID: p.Confirm, Name: p.Confirm}
			if b, err := json.Marshal(map[string]any{"id": 1, "method": "stack.import", "params": st.params}); err != nil || len(b) >= broker.MaxLine {
				it.Detail = "the spec and its secrets are too large to send in one request"
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonTooLarge})
				return res, nil
			}
			err := d.Broker.Call(ctx, "stack.import", st.params, nil)
			var be *broker.Error
			switch {
			case err == nil:
				res.Done = len(p.Items)
			case errors.Is(err, broker.ErrUnauthorized):
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonUnauthorized})
			case errors.As(err, &be) && (be.Code == broker.CodeBadParams || be.Code == broker.CodeRefused):
				it.Detail = be.Message
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonRefused})
			case errors.As(err, &be) && (be.Code == broker.CodeFailed || be.Code == broker.CodeConflict || be.Code == broker.CodeNotFound):
				it.Detail = strings.TrimPrefix(be.Message, "docker: ")
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
			default:
				return res, brokerError(err)
			}
			return res, nil
		}})
}

func planImport(ctx context.Context, d Deps, r actions.Request) (actions.Plan, error) {
	p := actions.Plan{Summary: "import " + r.Target, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	res, err := DryRun(ctx, d, r.Target, set(r.Params["plain"]), set(r.Params["skip"]))
	switch {
	case errors.Is(err, ErrNoProject):
		p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: r.Target, Name: r.Target}, Reason: ReasonNotFound})
		return p, nil
	case err != nil:
		return p, brokerError(err)
	}
	for _, why := range res.Refused {
		p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: r.Target, Name: r.Target, Detail: why}, Reason: ReasonRefused})
	}
	if len(p.Blocked) > 0 {
		return p, nil // nothing is imported unless all of it can be
	}
	for _, c := range res.Containers {
		p.Items = append(p.Items, actions.Item{ID: "container:" + c.Name, Name: c.Name, Detail: "recreated once, as " + c.Service + " of the app"})
	}
	for _, v := range res.Volumes {
		detail := "reused in place"
		if !v.Exists {
			detail = "created empty"
		}
		p.Items = append(p.Items, actions.Item{ID: "volume:" + v.Name, Name: v.Name, Detail: detail})
	}
	for _, root := range res.Spec.BindRoots {
		p.Items = append(p.Items, actions.Item{ID: "bind:" + root, Name: root, Detail: "bound in place, approved for this app"})
	}
	if n := len(res.Secrets); n > 0 {
		p.Items = append(p.Items, actions.Item{ID: "secrets", Name: fmt.Sprintf("%d secrets", n), Detail: "moved into the app's secrets.env, not shown"})
	}
	for _, f := range res.Flags {
		p.Items = append(p.Items, actions.Item{ID: "flag:" + f, Name: f, Detail: "danger: kept as the project runs it today"})
	}
	for _, ch := range res.Changes {
		p.Items = append(p.Items, actions.Item{ID: "change:" + ch, Name: "change", Detail: ch})
	}
	p.Items = append(p.Items, actions.Item{ID: "files", Name: strings.Join(res.Files, ", "),
		Detail: "left as they are; once imported, run the app from Hearth, not from these files"})
	p.Confirm = r.Target
	flags := res.Flags
	if flags == nil {
		flags = []string{}
	}
	raw, err := json.Marshal(res.Deploy)
	if err != nil {
		return p, actions.Errorf(http.StatusInternalServerError, "the spec could not be encoded")
	}
	values := res.Values
	if values == nil {
		values = map[string]string{}
	}
	p.State = importState{params: broker.StackImportParams{Spec: raw, AckFlags: flags, Secrets: values}}
	return p, nil
}

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
