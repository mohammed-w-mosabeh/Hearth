package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Kind is the actions' kind: proxy/listen and proxy/route.
const Kind = "proxy"

// Target of proxy/listen.
const listenTarget = "proxy"

// Register adds proxy/listen (target "proxy"; params enabled "true" or
// "false", and port) and proxy/route (target an app; param port, "0" for
// none). Both are reversible and audited by the registry.
func (p *Proxy) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: Kind, Name: "listen", Risk: actions.Reversible,
		Validate: func(r actions.Request) error { _, err := p.listenParams(r); return err },
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			c, err := p.listenParams(r)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%s", err.Error())
			}
			pl := actions.Plan{Summary: "stop serving app names", Items: []actions.Item{}, Blocked: []actions.Blocked{}, State: c}
			if c.Enabled {
				pl.Summary = "serve app names at http://<app>.localhost:" + strconv.Itoa(c.Port) + "/, on this computer only"
				for name, port := range c.Routes {
					pl.Items = append(pl.Items, actions.Item{ID: name, Name: name, Detail: nameURL(name, c.Port) + " → 127.0.0.1:" + strconv.Itoa(port)})
				}
			}
			return pl, nil
		},
		Run: func(ctx context.Context, _ actions.Request, pl actions.Plan) (actions.Result, error) {
			return p.apply(ctx, pl)
		}})
	reg.Register(actions.Action{Kind: Kind, Name: "route", Risk: actions.Reversible,
		Validate: func(r actions.Request) error { _, _, err := routeParams(r); return err },
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			name, port, err := routeParams(r)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%s", err.Error())
			}
			c := p.Config()
			pl := actions.Plan{Items: []actions.Item{{ID: name, Name: name}}, Blocked: []actions.Blocked{}}
			if port == 0 {
				delete(c.Routes, name)
				pl.Summary = "remove the name " + name + ".localhost"
				pl.State = c
				return pl, nil
			}
			ports, err := p.reachable(ctx, true)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusBadGateway, "cannot read the apps")
			}
			if !contains(ports[name], port) {
				pl.Blocked = append(pl.Blocked, blocked(name, name+" publishes no TCP port "+strconv.Itoa(port)+" on this computer"))
				pl.Items = []actions.Item{}
				return pl, nil
			}
			c.Routes[name] = port
			if err := c.Check(p.HearthPort()); err != nil {
				pl.Blocked = append(pl.Blocked, blocked(name, err.Error()))
				pl.Items = []actions.Item{}
				return pl, nil
			}
			pl.Summary = nameURL(name, c.Port) + " opens 127.0.0.1:" + strconv.Itoa(port)
			if !c.Enabled {
				pl.Summary += " once app names are served"
			}
			pl.State = c
			return pl, nil
		},
		Run: func(ctx context.Context, _ actions.Request, pl actions.Plan) (actions.Result, error) {
			return p.apply(ctx, pl)
		}})
}

func (p *Proxy) apply(ctx context.Context, pl actions.Plan) (actions.Result, error) {
	c, ok := pl.State.(Config)
	if !ok {
		return actions.Result{Failed: []actions.Blocked{}, Blocked: pl.Blocked}, nil
	}
	if err := p.Apply(ctx, c); err != nil {
		return actions.Result{Failed: []actions.Blocked{blocked(listenTarget, err.Error())}, Blocked: []actions.Blocked{}}, nil
	}
	return actions.Result{Done: 1, Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}, nil
}

func (p *Proxy) listenParams(r actions.Request) (Config, error) {
	if r.Target != listenTarget {
		return Config{}, errors.New(`target must be "proxy"`)
	}
	c := p.Config()
	for k, v := range r.Params {
		switch k {
		case "enabled":
			if v != "true" && v != "false" {
				return Config{}, errors.New(`params.enabled is "true" or "false"`)
			}
			c.Enabled = v == "true"
		case "port":
			n, err := strconv.Atoi(v)
			if err != nil || strconv.Itoa(n) != v {
				return Config{}, errors.New("params.port is a number")
			}
			c.Port = n
		default:
			return Config{}, errors.New("unknown param " + strconv.Quote(k))
		}
	}
	return c, c.Check(p.HearthPort())
}

func routeParams(r actions.Request) (string, int, error) {
	if !spec.ValidName(r.Target) {
		return "", 0, errors.New("target must be an app name")
	}
	if len(r.Params) != 1 {
		return "", 0, errors.New("the only param is port")
	}
	v, ok := r.Params["port"]
	n, err := strconv.Atoi(v)
	if !ok || err != nil || strconv.Itoa(n) != v || n < 0 || n > MaxPort {
		return "", 0, errors.New("params.port is 0 (none) or a port")
	}
	return r.Target, n, nil
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func nameURL(name string, port int) string {
	return "http://" + name + ".localhost:" + strconv.Itoa(port) + "/"
}

// HandleStatus serves GET /api/v1/proxy.
func (p *Proxy) HandleStatus(w http.ResponseWriter, r *http.Request) {
	st := p.Status(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func blocked(id, why string) actions.Blocked {
	return actions.Blocked{Item: actions.Item{ID: id, Name: id, Detail: why}, Reason: "refused"}
}
