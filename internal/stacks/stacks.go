// Package stacks is hearthd's side of the apps (ROADMAP 9.4): the stack
// actions behind the actions endpoint, each a broker call, and the status
// routes. The broker checks everything again; the previews here show what
// it will be asked, so what the owner confirms is what runs (SECURITY S15).
package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Broker calls hearth-broker.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Reasons in a result's failed list.
const (
	ReasonUnauthorized = "unauthorized" // polkit refused or the dialog was cancelled
	ReasonNotFound     = "not-found"    // no such stack
	ReasonRefused      = "refused"      // the broker refused it (Detail says why)
	ReasonError        = "error"        // anything else (Detail: the error)
)

// Kind is the actions' kind: POST /api/v1/actions/stack/{action}.
const Kind = "stack"

type deployState struct{ Spec json.RawMessage }

type updateState struct{ Params broker.StackUpdateParams }

type removeState struct{ Params broker.StackRemoveParams }

// Register adds stack/deploy, stack/start, stack/stop, stack/restart,
// stack/update and stack/remove.
func Register(reg *actions.Registry, b Broker, tags docker.TagChecker) {
	reg.Register(actions.Action{Kind: Kind, Name: "deploy", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a stack name")
			}
			for k := range r.Params {
				if k != "spec" {
					return fmt.Errorf("unknown param %q", k)
				}
			}
			if s := r.Params["spec"]; s == "" || len(s) > spec.MaxSpec {
				return errors.New("params.spec must be a spec of at most 48 KiB")
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) { return planDeploy(r) },
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(deployState)
			if !ok {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.deploy", broker.StackDeployParams{Spec: st.Spec, AckFlags: []string{}})
		}})
	for _, op := range []struct {
		verb string
		risk actions.Risk
	}{{"start", actions.Safe}, {"stop", actions.Reversible}, {"restart", actions.Reversible}} {
		reg.Register(actions.Action{Kind: Kind, Name: op.verb, Risk: op.risk,
			Validate: nameOnly,
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
				info, p, err := find(ctx, b, r.Target, op.verb)
				if err != nil || len(p.Blocked) > 0 {
					return p, err
				}
				p.Items = containerItems(info)
				if len(p.Items) == 0 {
					p.Items = []actions.Item{{ID: info.Name, Name: info.Name}}
				}
				return p, nil
			},
			Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
				if len(p.Blocked) > 0 {
					return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
				}
				return call(ctx, b, p, "stack.control", broker.StackControlParams{Name: r.Target, Verb: op.verb})
			}})
	}
	reg.Register(actions.Action{Kind: Kind, Name: "update", Risk: actions.Destructive,
		Validate: nameOnly,
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planUpdate(ctx, b, tags, r.Target)
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(updateState)
			if !ok {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.update", st.Params)
		}})
	reg.Register(actions.Action{Kind: Kind, Name: "remove", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a stack name")
			}
			for k, v := range r.Params {
				if k != "volumes" || (v != "true" && v != "false") {
					return errors.New(`the only param is volumes, "true" or "false"`)
				}
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planRemove(ctx, b, r.Target, r.Params["volumes"] == "true")
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(removeState)
			if !ok {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.remove", st.Params)
		}})
}

func nameOnly(r actions.Request) error {
	if !spec.ValidName(r.Target) {
		return errors.New("target must be a stack name")
	}
	if len(r.Params) != 0 {
		return errors.New("params must be empty")
	}
	return nil
}

func planDeploy(r actions.Request) (actions.Plan, error) {
	p := actions.Plan{Summary: "deploy " + r.Target, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	raw := json.RawMessage(r.Params["spec"])
	sp, rep, err := spec.Parse(raw)
	switch {
	case err != nil:
		p.Blocked = append(p.Blocked, blocked(r.Target, err.Error()))
		return p, nil
	case sp.Name != r.Target:
		p.Blocked = append(p.Blocked, blocked(r.Target, "the spec's name is "+sp.Name))
		return p, nil
	}
	for _, why := range rep.Refused {
		p.Blocked = append(p.Blocked, blocked(r.Target, why))
	}
	for _, f := range rep.Flags {
		p.Blocked = append(p.Blocked, blocked(r.Target, "danger flag "+f+": not deployable before task 9.6"))
	}
	if len(p.Blocked) > 0 {
		return p, nil
	}
	names := make([]string, 0, len(sp.Services))
	for n := range sp.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p.Items = append(p.Items, actions.Item{ID: n, Name: n, Detail: describe(sp.Services[n])})
	}
	p.State = deployState{Spec: raw}
	return p, nil
}

// describe is one line per service: image, ports and binds, what deploying
// it exposes and mounts.
func describe(s spec.Service) string {
	parts := []string{s.Image}
	for _, pt := range s.Ports {
		ip := pt.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		parts = append(parts, "port "+ip+":"+strconv.Itoa(pt.Host)+"→"+strconv.Itoa(pt.Container))
	}
	for _, v := range s.Volumes {
		if v.Type == "bind" {
			parts = append(parts, "bind "+v.Source+"→"+v.Target)
		}
	}
	restart := s.Restart
	if restart == "" {
		restart = spec.DefaultRestart
	}
	return strings.Join(append(parts, "restart "+restart), ", ")
}

func blocked(id, why string) actions.Blocked {
	return actions.Blocked{Item: actions.Item{ID: id, Name: id, Detail: why}, Reason: ReasonRefused}
}

// find reads one stack's status; a missing stack is a blocked item.
func find(ctx context.Context, b Broker, name, verb string) (broker.StackInfo, actions.Plan, error) {
	p := actions.Plan{Summary: verb + " " + name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	var list []broker.StackInfo
	if err := b.Call(ctx, "stack.status", map[string]string{"name": name}, &list); err != nil {
		var be *broker.Error
		if errors.As(err, &be) && be.Code == broker.CodeNotFound {
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: name, Name: name}, Reason: ReasonNotFound})
			return broker.StackInfo{}, p, nil
		}
		return broker.StackInfo{}, p, brokerError(err)
	}
	if len(list) != 1 {
		return broker.StackInfo{}, p, actions.Errorf(http.StatusBadGateway, "broker: unexpected status")
	}
	return list[0], p, nil
}

func containerItems(info broker.StackInfo) []actions.Item {
	out := []actions.Item{}
	for _, c := range info.Containers {
		out = append(out, actions.Item{ID: c.ID, Name: c.Name, Detail: c.Service + ": " + c.State})
	}
	return out
}

func planUpdate(ctx context.Context, b Broker, tags docker.TagChecker, name string) (actions.Plan, error) {
	info, p, err := find(ctx, b, name, "update")
	if err != nil || len(p.Blocked) > 0 {
		return p, err
	}
	var sp spec.Spec
	if err := json.Unmarshal(info.Spec, &sp); err != nil {
		return p, actions.Errorf(http.StatusBadGateway, "broker: unexpected spec")
	}
	images := map[string]string{} // service to image ID
	for _, c := range info.Containers {
		images[c.Service] = c.ImageID
	}
	pins := map[string]string{}
	names := make([]string, 0, len(sp.Services))
	for n := range sp.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := sp.Services[n]
		_, _, digest, ok := broker.ParseImageRef(svc.Image)
		switch {
		case !ok:
			continue
		case svc.PullPolicy == "never":
			p.Items = append(p.Items, actions.Item{ID: n, Name: n, Detail: svc.Image + ": local image, not updated"})
			continue
		case digest != "":
			p.Items = append(p.Items, actions.Item{ID: n, Name: n, Detail: svc.Image + ": pinned by digest, not updated"})
			continue
		}
		detail, remote := digestChange(ctx, b, tags, images[n], svc.Image)
		if remote != "" {
			pins[n] = remote
		}
		p.Items = append(p.Items, actions.Item{ID: n, Name: n, Detail: svc.Image + ": " + detail})
	}
	if len(pins) == 0 {
		p.Summary = name + " is up to date, or its registries could not be asked"
		return p, nil
	}
	p.State = updateState{Params: broker.StackUpdateParams{Name: name, SpecSHA256: info.SpecSHA256, Pins: pins}}
	return p, nil
}

// digestChange is 7.6's check: it reads the running image's repo digests and
// asks the registry, and returns the new digest only when there is one.
func digestChange(ctx context.Context, b Broker, tags docker.TagChecker, imageID, image string) (string, string) {
	if imageID == "" {
		return "not running, digest unknown", ""
	}
	var in struct{ RepoDigests []string }
	if err := b.Call(ctx, "docker.images.inspect", map[string]string{"id": imageID}, &in); err != nil {
		return "digest unknown", ""
	}
	got := tags.Check(ctx, []string{image}, in.RepoDigests)
	if len(got) != 1 {
		return "digest unknown", ""
	}
	switch got[0].Status {
	case docker.UpdateCurrent:
		return "digest unchanged", ""
	case docker.UpdateNewer:
		return "new digest " + short(got[0].Remote), got[0].Remote
	case docker.UpdateLocal:
		return "built here or never pulled", ""
	}
	return "digest unknown", ""
}

func short(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

func planRemove(ctx context.Context, b Broker, name string, volumes bool) (actions.Plan, error) {
	info, p, err := find(ctx, b, name, "remove")
	if err != nil || len(p.Blocked) > 0 {
		return p, err
	}
	p.Items = containerItems(info)
	params := broker.StackRemoveParams{Name: name}
	if volumes {
		var sp spec.Spec
		if err := json.Unmarshal(info.Spec, &sp); err != nil {
			return p, actions.Errorf(http.StatusBadGateway, "broker: unexpected spec")
		}
		keys := make([]string, 0, len(sp.Volumes))
		for k, v := range sp.Volumes {
			if !v.External {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			vol := name + "_" + k
			if n := sp.Volumes[k].Name; n != "" {
				vol = n
			}
			p.Items = append(p.Items, actions.Item{ID: "volume:" + vol, Name: vol, Detail: "volume, its data is deleted"})
		}
		p.Summary = "remove " + name + " and delete its volumes"
		p.Confirm = name
		params.RemoveVolumes, params.TypedName = true, name
	} else {
		p.Summary = "remove " + name + "; its volumes and bind folders stay"
	}
	if len(p.Items) == 0 {
		p.Items = []actions.Item{{ID: name, Name: name}}
	}
	p.State = removeState{Params: params}
	return p, nil
}

// call runs one broker method for the whole plan.
func call(ctx context.Context, b Broker, p actions.Plan, method string, params any) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
	err := b.Call(ctx, method, params, nil)
	if err == nil {
		res.Done = len(p.Items)
		return res, nil
	}
	it := actions.Item{ID: p.Summary, Name: p.Summary}
	var be *broker.Error
	switch {
	case errors.Is(err, broker.ErrUnauthorized):
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonUnauthorized})
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonNotFound})
	case errors.As(err, &be) && (be.Code == broker.CodeRefused || be.Code == broker.CodeBadParams):
		it.Detail = be.Message
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonRefused})
	case errors.As(err, &be) && (be.Code == broker.CodeFailed || be.Code == broker.CodeConflict):
		it.Detail = strings.TrimPrefix(be.Message, "docker: ")
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
	default:
		return res, brokerError(err)
	}
	return res, nil
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

// HandleList serves GET /api/v1/stacks: every stack's status.
func HandleList(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var list []broker.StackInfo
		if err := b.Call(r.Context(), "stack.status", nil, &list); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, list)
	}
}

// HandleOne serves GET /api/v1/stacks/{name}.
func HandleOne(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !spec.ValidName(name) {
			jsonError(w, http.StatusBadRequest, "not a stack name")
			return
		}
		var list []broker.StackInfo
		if err := b.Call(r.Context(), "stack.status", map[string]string{"name": name}, &list); err != nil {
			httpError(w, err)
			return
		}
		if len(list) != 1 {
			jsonError(w, http.StatusBadGateway, "unexpected status")
			return
		}
		writeJSON(w, list[0])
	}
}

func httpError(w http.ResponseWriter, err error) {
	var be *broker.Error
	status, msg := http.StatusBadGateway, "the broker could not be asked"
	switch {
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		status, msg = http.StatusNotFound, "no such stack"
	case errors.Is(err, broker.ErrUnavailable), errors.Is(err, broker.ErrNoDocker):
		status, msg = http.StatusServiceUnavailable, "hearth-broker or docker is not available"
	case errors.Is(err, broker.ErrNotMember):
		status, msg = http.StatusForbidden, "you are not in the hearth group"
	}
	jsonError(w, status, msg)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// Auditor records what hearthd did (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// secretsBody is POST /api/v1/stacks/{name}/secrets's body.
type secretsBody struct {
	Set    map[string]string `json:"set"`
	Remove []string          `json:"remove"`
}

// HandleSecretsSet serves POST /api/v1/stacks/{name}/secrets: the broker
// asks for the password (SECURITY S12c). The audit holds key names only.
func HandleSecretsSet(b Broker, audit Auditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !spec.ValidName(name) {
			jsonError(w, http.StatusBadRequest, "not a stack name")
			return
		}
		var v secretsBody
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*secrets.MaxTotal))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil || dec.More() {
			jsonError(w, http.StatusBadRequest, "the body is {\"set\": {KEY: value}, \"remove\": [KEY]}")
			return
		}
		keys := make([]string, 0, len(v.Set))
		for k := range v.Set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		err := b.Call(r.Context(), "stack.secrets.set", broker.StackSecretsSetParams{Name: name, Set: v.Set, Remove: v.Remove}, nil)
		auditCall(r.Context(), audit, "stack.secrets.set", name, map[string]any{"set": keys, "remove": v.Remove}, err)
		if err != nil {
			secretError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"done": true})
	}
}

// HandleReveal serves POST /api/v1/stacks/{name}/secrets/{key}/reveal: one
// value, after the password, never cached.
func HandleReveal(b Broker, audit Auditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, key := r.PathValue("name"), r.PathValue("key")
		if !spec.ValidName(name) || !secrets.ValidKey(key) {
			jsonError(w, http.StatusBadRequest, "not a stack or secret name")
			return
		}
		var out struct {
			Value string `json:"value"`
		}
		err := b.Call(r.Context(), "stack.secrets.reveal", broker.StackSecretRevealParams{Name: name, Key: key}, &out)
		auditCall(r.Context(), audit, "stack.secrets.reveal", name, map[string]string{"key": key}, err)
		if err != nil {
			secretError(w, err)
			return
		}
		writeJSON(w, out)
	}
}

func auditCall(ctx context.Context, audit Auditor, action, target string, params any, err error) {
	result := "ok"
	if err != nil {
		result = "error"
		if errors.Is(err, broker.ErrUnauthorized) {
			result = "refused: not authorized"
		}
	}
	_ = audit.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action, Target: target, Params: params, Result: result})
}

// secretError answers a broker refusal with its reason, which names keys
// and limits but never a value.
func secretError(w http.ResponseWriter, err error) {
	var be *broker.Error
	switch {
	case errors.Is(err, broker.ErrUnauthorized):
		jsonError(w, http.StatusForbidden, "not authorized")
	case errors.As(err, &be) && (be.Code == broker.CodeBadParams || be.Code == broker.CodeRefused):
		jsonError(w, http.StatusUnprocessableEntity, be.Message)
	default:
		httpError(w, err)
	}
}
