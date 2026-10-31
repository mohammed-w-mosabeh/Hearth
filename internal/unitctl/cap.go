package unitctl

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
)

// ReasonCritical is a unit that keeps the session running: a limit on it
// could freeze the desktop, so it is refused rather than confirmed.
const ReasonCritical = "critical"

// minCapMemory keeps a cap from killing what it is put on at once.
const minCapMemory = 16 << 20

var capQuota = regexp.MustCompile(`^([1-9][0-9]{0,4})%$`)

// property is one (sv) of SetUnitProperties.
type property struct {
	Name  string
	Value dbus.Variant
}

// capProps turns {MemoryMax, CPUQuota} into the properties to set; a limit
// left out goes back to none, so one form both sets and clears.
func capProps(p map[string]string) ([]property, error) {
	mem, quota := uint64(math.MaxUint64), uint64(math.MaxUint64)
	for k, v := range p {
		switch k {
		case "MemoryMax":
			n, err := broker.ParseBytes(v)
			if err != nil {
				return nil, err
			}
			if n < minCapMemory {
				return nil, errors.New("a memory limit must be 16M or more")
			}
			mem = uint64(n) // #nosec G115 -- ParseBytes returns a positive size
		case "CPUQuota":
			m := capQuota.FindStringSubmatch(v)
			if m == nil {
				return nil, errors.New("a CPU limit is a percentage like 150%")
			}
			pct, err := strconv.ParseUint(m[1], 10, 64)
			if err != nil {
				return nil, err
			}
			quota = pct * 10_000 // microseconds of CPU per second
		default:
			return nil, fmt.Errorf("%q cannot be limited", k)
		}
	}
	return []property{
		{"MemoryMax", dbus.MakeVariant(mem)},
		{"CPUQuotaPerSecUSec", dbus.MakeVariant(quota)},
	}, nil
}

// capTarget accepts the user's own running services and scopes.
func capTarget(s string) (string, error) {
	scope, name, err := Target(s)
	if err != nil {
		return "", err
	}
	if scope != services.ScopeUser {
		return "", errors.New("only your own units can be capped; a system service's limits are saved with Limits…")
	}
	if !strings.HasSuffix(name, ".service") && !strings.HasSuffix(name, ".scope") {
		return "", errors.New("only a service or a scope has limits")
	}
	return name, nil
}

// capAction is unit/cap: limits until the unit ends or the session does
// (runtime only, nothing is written to disk).
func (c *ctl) capAction() actions.Action {
	return actions.Action{Kind: "unit", Name: "cap", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if _, err := capTarget(r.Target); err != nil {
				return err
			}
			_, err := capProps(r.Params)
			return err
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			name, err := capTarget(r.Target)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%v", err)
			}
			it := actions.Item{ID: r.Target, Name: name}
			p := actions.Plan{Summary: "cap " + name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
			if services.Critical(services.ScopeUser, name) {
				p.Blocked = append(p.Blocked, actions.Blocked{Item: it, Reason: ReasonCritical})
				return p, nil
			}
			p.Items = append(p.Items, it)
			return p, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
			if len(p.Items) == 0 {
				return res, nil
			}
			props, err := capProps(r.Params)
			if err != nil {
				return res, err
			}
			bus, err := c.cfg.User(ctx)
			if err != nil {
				return res, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the user manager: %v", err)
			}
			defer func() { _ = bus.Close() }()
			if err := bus.CallInteractive(ctx, systemd, objPath, manager+".SetUnitProperties", nil,
				p.Items[0].Name, true, props); err != nil {
				res.Failed = append(res.Failed, failure(p.Items[0], err))
				return res, nil
			}
			res.Done = 1
			return res, nil
		}}
}
