package settings

import (
	"context"
	"errors"
	"net/http"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Autostart lists and changes autostart entries (discovery.Autostart).
type Autostart interface {
	List() []discovery.AutostartEntry
	Set(id string, enabled bool) error
	Add(appID string) error
}

func (s *Settings) registerAutostart(reg *actions.Registry) {
	if s.cfg.Autostart == nil {
		return
	}
	find := func(id string) (discovery.AutostartEntry, bool) {
		for _, e := range s.cfg.Autostart.List() {
			if e.ID == id {
				return e, true
			}
		}
		return discovery.AutostartEntry{}, false
	}
	reg.Register(actions.Action{Kind: "autostart", Name: "set", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if on := r.Params["enabled"]; !discovery.ValidID(r.Target) || len(r.Params) != 1 || (on != "true" && on != "false") {
				return errors.New(`target must be an autostart entry and params {"enabled": "true" or "false"}`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			e, ok := find(r.Target)
			if !ok {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no autostart entry %s", r.Target)
			}
			verb := "stop starting "
			if r.Params["enabled"] == "true" {
				verb = "start "
			}
			return one(verb+e.Name+" at login", e.ID, e.Name), nil
		},
		Run: func(_ context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return done(p, s.cfg.Autostart.Set(r.Target, r.Params["enabled"] == "true"))
		}})
	reg.Register(actions.Action{Kind: "autostart", Name: "add", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if !discovery.ValidID(r.Target) || len(r.Params) != 0 {
				return errors.New("target must be an application ID and params empty")
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return one("start "+r.Target+" at login", r.Target, r.Target), nil
		},
		Run: func(_ context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return done(p, s.cfg.Autostart.Add(r.Target))
		}})
}

// done is the result of a change made without D-Bus.
func done(p actions.Plan, err error) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	if err != nil {
		it := p.Items[0]
		it.Detail = err.Error()
		reason := ReasonError
		if errors.Is(err, discovery.ErrNoEntry) {
			reason = ReasonUnavailable
		}
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: reason})
		return res, nil
	}
	res.Done = 1
	return res, nil
}
