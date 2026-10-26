// Package unitctl implements the service actions (SPEC 10, ROADMAP 1.13):
// start, stop, restart, enable and disable systemd units, system or user,
// through the managers' D-Bus APIs. hearthd never runs as root: for system
// units systemd asks polkit, which shows the desktop's authentication
// dialog (the calls allow interactive authorization).
//
// Stopping, restarting or disabling a critical unit (services.Critical) is
// raised to destructive: it needs a preview and the unit's name typed back
// (SECURITY S16). hearthd's own unit is never stopped or disabled from
// its UI.
package unitctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
)

const (
	systemd = "org.freedesktop.systemd1"
	manager = "org.freedesktop.systemd1.Manager"
	objPath = dbus.ObjectPath("/org/freedesktop/systemd1")
)

// Reasons an item is blocked or failed; the UI translates them.
const (
	ReasonSelf         = "self"         // hearthd's own unit
	ReasonUnauthorized = "unauthorized" // polkit refused or the dialog was cancelled
	ReasonNotFound     = "not-found"    // no such unit
	ReasonMasked       = "masked"       // masked units cannot start
	ReasonNoInstall    = "no-install"   // no [Install] section: cannot be enabled
	ReasonJobFailed    = "job-failed"   // the job ended badly (Detail: systemd's result)
	ReasonError        = "error"        // anything else (Detail: the error)
)

// Bus is a manager connection (dbusx.Conn in production).
type Bus interface {
	dbusx.Bus
	CallInteractive(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Close() error
}

// Config configures the unit actions.
type Config struct {
	System, User func(ctx context.Context) (Bus, error)
	// Self is hearthd's own unit as a target ("user/hearthd.service").
	Self string
	// JobWait bounds how long a start/stop/restart is followed; a job
	// still running then is reported as done (the list shows the rest).
	JobWait time.Duration
}

type ctl struct{ cfg Config }

// Target parses "<scope>/<unit>".
func Target(s string) (scope, name string, err error) {
	scope, name, ok := strings.Cut(s, "/")
	if !ok || (scope != services.ScopeSystem && scope != services.ScopeUser) {
		return "", "", errors.New(`target must be "system/<unit>" or "user/<unit>"`)
	}
	if err := safeexec.UnitName(name); err != nil {
		return "", "", err
	}
	if strings.Contains(name, "@.") {
		return "", "", errors.New("a template unit cannot be acted on; pick an instance")
	}
	return scope, name, nil
}

type op struct {
	name     string
	risk     actions.Risk
	escalate bool // critical units need the typed confirmation
	self     bool // refused on hearthd's own unit
}

var ops = []op{
	{name: "start", risk: actions.Safe},
	{name: "stop", risk: actions.Reversible, escalate: true, self: true},
	{name: "restart", risk: actions.Reversible, escalate: true, self: true},
	{name: "enable", risk: actions.Reversible},
	{name: "disable", risk: actions.Reversible, escalate: true, self: true},
}

// Register adds the unit/* actions to reg.
func Register(reg *actions.Registry, cfg Config) {
	if cfg.Self == "" {
		cfg.Self = "user/hearthd.service"
	}
	if cfg.JobWait == 0 {
		cfg.JobWait = 30 * time.Second
	}
	c := &ctl{cfg: cfg}
	for _, o := range ops {
		reg.Register(actions.Action{Kind: "unit", Name: o.name, Risk: o.risk,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				_, _, err := Target(r.Target)
				return err
			},
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) { return c.plan(o, r.Target) },
			Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
				return c.run(ctx, o, r.Target, p)
			}})
	}
}

func (c *ctl) plan(o op, target string) (actions.Plan, error) {
	scope, name, err := Target(target)
	if err != nil {
		return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%v", err)
	}
	it := actions.Item{ID: target, Name: name, Detail: scope}
	p := actions.Plan{Summary: o.name + " " + name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if o.self && target == c.cfg.Self {
		p.Blocked = append(p.Blocked, actions.Blocked{Item: it, Reason: ReasonSelf})
		return p, nil
	}
	p.Items = append(p.Items, it)
	if o.escalate && services.Critical(scope, name) {
		p.Risk, p.Confirm = actions.Destructive, name
	}
	return p, nil
}

func (c *ctl) run(ctx context.Context, o op, target string, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	if len(p.Items) == 0 {
		return res, nil
	}
	scope, name, err := Target(target)
	if err != nil {
		return res, err
	}
	dial := c.cfg.System
	if scope == services.ScopeUser {
		dial = c.cfg.User
	}
	bus, err := dial(ctx)
	if err != nil {
		return res, actions.Errorf(http.StatusServiceUnavailable, "cannot reach the %s manager: %v", scope, err)
	}
	defer func() { _ = bus.Close() }()

	switch o.name {
	case "start", "stop", "restart":
		err = c.job(ctx, bus, strings.ToUpper(o.name[:1])+o.name[1:]+"Unit", name)
	case "enable":
		var carries bool
		var changes []change
		if err = bus.CallInteractive(ctx, systemd, objPath, manager+".EnableUnitFiles", []any{&carries, &changes},
			[]string{name}, false, false); err == nil {
			if !carries && len(changes) == 0 {
				err = errNoInstall
			} else {
				err = c.reload(ctx, bus)
			}
		}
	case "disable":
		var changes []change
		if err = bus.CallInteractive(ctx, systemd, objPath, manager+".DisableUnitFiles", []any{&changes},
			[]string{name}, false); err == nil {
			err = c.reload(ctx, bus)
		}
	}
	if err != nil {
		res.Failed = append(res.Failed, failure(p.Items[0], err))
		return res, nil
	}
	res.Done = 1
	return res, nil
}

// change is one entry of EnableUnitFiles/DisableUnitFiles' a(sss).
type change struct {
	Type, Filename, Destination string
}

var errNoInstall = errors.New("no [Install] section")

// jobError is a job that ended with a result other than "done".
type jobError struct{ result string }

func (e *jobError) Error() string { return "job " + e.result }

// job runs method (StartUnit, StopUnit, RestartUnit) and follows the job
// to its end.
func (c *ctl) job(ctx context.Context, bus Bus, method, name string) error {
	wctx, cancel := context.WithTimeout(ctx, dbusx.InteractiveTimeout+c.cfg.JobWait)
	defer cancel()
	sigs, err := bus.Watch(wctx, dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(manager), dbus.WithMatchMember("JobRemoved"))
	if err != nil {
		return err
	}
	if err := bus.Call(wctx, systemd, objPath, manager+".Subscribe", nil); err != nil {
		return err
	}
	var job dbus.ObjectPath
	if err := bus.CallInteractive(wctx, systemd, objPath, manager+"."+method, []any{&job}, name, "replace"); err != nil {
		return err
	}
	// Authorization is over once the call returns: from here only the
	// job itself is waited for.
	timeout := time.After(c.cfg.JobWait)
	for {
		select {
		case <-timeout:
			return nil // still running; the Services list follows it
		case <-wctx.Done():
			return nil
		case s, ok := <-sigs:
			if !ok {
				return errors.New("lost the connection to systemd")
			}
			if len(s.Body) != 4 {
				continue
			}
			if p, _ := s.Body[1].(dbus.ObjectPath); p != job {
				continue
			}
			if r, _ := s.Body[3].(string); r != "done" {
				return &jobError{result: r}
			}
			return nil
		}
	}
}

// reload is systemctl daemon-reload, as systemctl enable/disable do.
func (c *ctl) reload(ctx context.Context, bus Bus) error {
	return bus.CallInteractive(ctx, systemd, objPath, manager+".Reload", nil)
}

// failure turns an error into a failed item with a reason.
func failure(it actions.Item, err error) actions.Blocked {
	var je *jobError
	switch {
	case errors.As(err, &je):
		it.Detail = je.result
		return actions.Blocked{Item: it, Reason: ReasonJobFailed}
	case errors.Is(err, errNoInstall):
		return actions.Blocked{Item: it, Reason: ReasonNoInstall}
	}
	switch dbusx.ErrorName(err) {
	case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired",
		"org.freedesktop.PolicyKit1.Error.NotAuthorized", "org.freedesktop.PolicyKit1.Error.Cancelled":
		return actions.Blocked{Item: it, Reason: ReasonUnauthorized}
	case "org.freedesktop.systemd1.NoSuchUnit", "org.freedesktop.systemd1.LoadFailed", "org.freedesktop.DBus.Error.FileNotFound":
		return actions.Blocked{Item: it, Reason: ReasonNotFound}
	case "org.freedesktop.systemd1.UnitMasked":
		return actions.Blocked{Item: it, Reason: ReasonMasked}
	}
	it.Detail = fmt.Sprint(errors.Unwrap(err))
	if it.Detail == "<nil>" {
		it.Detail = err.Error()
	}
	return actions.Blocked{Item: it, Reason: ReasonError}
}
