// Package firewall is the firewall section of the Network page (ROADMAP
// 7.2): ufw's rules, and a preview before each change. The broker validates
// and applies; this package says what a change will do, which is what the
// typed confirmation and the audit log rest on (SECURITY S15, S16h).
// nftables rule sets stay read-only and are never written from here.
package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Target is the only target: the one ufw.
const Target = "ufw"

// ConfirmDisable is what must be typed to turn the firewall off.
const ConfirmDisable = "disable firewall"

const sshPort = 22

// Reasons.
const (
	ReasonUnauthorized = "unauthorized"
	ReasonError        = "error"
	ReasonInactive     = "firewall-inactive"
	ReasonExists       = "firewall-exists"
	ReasonMissing      = "firewall-missing"
	ReasonNoTool       = "firewall-no-tool"
	ReasonSame         = "firewall-same"
)

// Broker is the part of broker.Client the package needs.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Mux is the subset of api.Mux the route needs.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

func state(ctx context.Context, b Broker) (broker.UfwState, error) {
	var s broker.UfwState
	err := b.Call(ctx, "ufw.state", nil, &s)
	return s, translate(err)
}

// translate keeps a broker failure's internals out of a response.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, broker.ErrUnavailable):
		return actions.Errorf(http.StatusServiceUnavailable, "hearth-broker is not available")
	case errors.Is(err, broker.ErrNotMember):
		return actions.Errorf(http.StatusForbidden, "you are not in the hearth group")
	}
	return actions.Errorf(http.StatusBadGateway, "the firewall state could not be read")
}

func parseRule(op string, r actions.Request) (broker.UfwRuleParams, error) {
	if r.Target != Target {
		return broker.UfwRuleParams{}, errors.New(`target must be "ufw"`)
	}
	for k := range r.Params {
		if k != "action" && k != "proto" && k != "port" && k != "source" {
			return broker.UfwRuleParams{}, errors.New("unknown param " + strconv.Quote(k))
		}
	}
	port, err := strconv.Atoi(r.Params["port"])
	if err != nil {
		return broker.UfwRuleParams{}, errors.New("port must be 1 to 65535")
	}
	return broker.CheckUfwRule(broker.UfwRuleParams{Op: op, Action: r.Params["action"], Proto: r.Params["proto"], Port: port, Source: r.Params["source"]})
}

// Describe is a rule in words.
func Describe(p broker.UfwRuleParams) string {
	source := "anywhere"
	if p.Source != "" {
		source = p.Source
	}
	verb := "Allow"
	if p.Action == "deny" {
		verb = "Deny"
	}
	return verb + " " + strconv.Itoa(p.Port) + "/" + p.Proto + " from " + source
}

func hasRule(s broker.UfwState, p broker.UfwRuleParams) bool {
	for _, r := range s.Rules {
		if r.Editable && r.Action == p.Action && r.Proto == p.Proto && r.Port == p.Port && r.Source == p.Source {
			return true
		}
	}
	return false
}

// sshAllowed reports a rule that lets anyone in on port 22, from anywhere.
func sshAllowed(s broker.UfwState) bool {
	return hasRule(s, broker.UfwRuleParams{Action: "allow", Proto: "tcp", Port: sshPort})
}

// lockout says what a change does to someone connected over SSH, or "".
func lockout(s broker.UfwState, p broker.UfwRuleParams) string {
	if p.Proto != "tcp" || p.Port != sshPort {
		return ""
	}
	if (p.Op == "add" && p.Action == "deny") || (p.Op == "delete" && p.Action == "allow" && p.Source == "") {
		return "Port 22 is SSH: this can cut off remote sessions to this computer"
	}
	return ""
}

func rulePlan(s broker.UfwState, p broker.UfwRuleParams) actions.Plan {
	summary := "add rule"
	if p.Op == "delete" {
		summary = "delete rule"
	}
	it := actions.Item{ID: Target, Name: Describe(p), Detail: lockout(s, p)}
	plan := actions.Plan{Summary: summary, Items: []actions.Item{}, Blocked: []actions.Blocked{}, State: p}
	switch has := hasRule(s, p); {
	case !s.Installed:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonNoTool})
	case !s.Active:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonInactive})
	case p.Op == "add" && has:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonExists})
	case p.Op == "delete" && !has:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonMissing})
	default:
		plan.Items = append(plan.Items, it)
	}
	return plan
}

func setPlan(s broker.UfwState, enabled bool) actions.Plan {
	plan := actions.Plan{Items: []actions.Item{}, Blocked: []actions.Blocked{}, State: enabled}
	it := actions.Item{ID: Target, Name: "Turn the firewall on"}
	plan.Summary = "enable firewall"
	if !enabled {
		it.Name, plan.Summary, plan.Confirm = "Turn the firewall off", "disable firewall", ConfirmDisable
		it.Detail = "Every port something listens on is then open to the network"
	} else if s.Incoming == "deny" && !sshAllowed(s) {
		it.Detail = "Incoming connections are refused unless a rule allows them, and none allows SSH (22/tcp): remote sessions can be cut off"
	}
	switch {
	case !s.Installed:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonNoTool})
	case s.Active == enabled:
		plan.Blocked = append(plan.Blocked, actions.Blocked{Item: it, Reason: ReasonSame})
	default:
		plan.Items = append(plan.Items, it)
	}
	return plan
}

func result(p actions.Plan, err error) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
	if len(p.Items) == 0 {
		return res, nil
	}
	it := p.Items[0]
	var be *broker.Error
	switch {
	case err == nil:
		res.Done = 1
	case errors.Is(err, broker.ErrUnauthorized):
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonUnauthorized})
	case errors.As(err, &be) && (be.Code == broker.CodeFailed || be.Code == broker.CodeRefused):
		it.Detail = be.Message
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
	default:
		return res, translate(err)
	}
	return res, nil
}

// Register adds firewall/rule-add, rule-delete, enable and disable, all with
// a preview, and GET /api/v1/firewall.
func Register(reg *actions.Registry, mux Mux, b Broker) {
	for name, op := range map[string]string{"rule-add": "add", "rule-delete": "delete"} {
		reg.Register(actions.Action{Kind: "firewall", Name: name, Risk: actions.Destructive,
			Validate: func(r actions.Request) error { _, err := parseRule(op, r); return err },
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
				p, err := parseRule(op, r)
				if err != nil {
					return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%s", err.Error())
				}
				s, err := state(ctx, b)
				if err != nil {
					return actions.Plan{}, err
				}
				return rulePlan(s, p), nil
			},
			Run: func(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
				p, ok := plan.State.(broker.UfwRuleParams)
				if !ok || len(plan.Items) == 0 {
					return result(plan, nil)
				}
				return result(plan, b.Call(ctx, "ufw.rule", p, nil))
			}})
	}
	for name, enabled := range map[string]bool{"enable": true, "disable": false} {
		reg.Register(actions.Action{Kind: "firewall", Name: name, Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if r.Target != Target || len(r.Params) != 0 {
					return errors.New(`target must be "ufw" and params empty`)
				}
				return nil
			},
			Plan: func(ctx context.Context, _ actions.Request) (actions.Plan, error) {
				s, err := state(ctx, b)
				if err != nil {
					return actions.Plan{}, err
				}
				return setPlan(s, enabled), nil
			},
			Run: func(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
				if len(plan.Items) == 0 {
					return result(plan, nil)
				}
				return result(plan, b.Call(ctx, "ufw.set", broker.UfwSetParams{Enabled: enabled}, nil))
			}})
	}
	mux.HandleFunc("GET /api/v1/firewall", func(w http.ResponseWriter, r *http.Request) {
		s, err := state(r.Context(), b)
		if err != nil {
			var ae *actions.Error
			code := http.StatusBadGateway
			if errors.As(err, &ae) {
				code = ae.Status
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
