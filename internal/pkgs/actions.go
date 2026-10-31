package pkgs

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Reasons a package is blocked or failed (web/src/lib/actions.ts).
const (
	ReasonSelf         = "self"         // Hearth itself
	ReasonEssential    = "essential"    // Essential: yes
	ReasonProtected    = "protected"    // Protected: yes
	ReasonRequired     = "required"     // Priority: required
	ReasonUnauthorized = "unauthorized" // the polkit dialog was cancelled or refused
	ReasonError        = "error"        // anything else (Detail: PackageKit's message)
)

var undeletableReason = map[string]string{
	"hearth": ReasonSelf, "essential": ReasonEssential, "protected": ReasonProtected, "required": ReasonRequired,
}

// Installed is dpkg's view of installed packages (discovery.Dpkg).
type Installed interface {
	ByName(name string) ([]discovery.Package, error)
}

// Config configures the package actions.
type Config struct {
	PK   *Client
	Dpkg Installed
}

// nameRe is a Debian package name, optionally with ":arch".
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,127}(:[a-z0-9-]{1,32})?$`)

// Target parses "name" or "name:arch".
func Target(s string) (name, arch string, err error) {
	if !nameRe.MatchString(s) {
		return "", "", errors.New("target must be a Debian package name, optionally with :arch")
	}
	name, arch, _ = strings.Cut(s, ":")
	return name, arch, nil
}

// changing are the infos of a simulation that change the system.
var changing = map[string]bool{
	"installing": true, "removing": true, "updating": true, "obsoleting": true,
	"downgrading": true, "reinstalling": true,
}

func removes(info string) bool { return info == "removing" || info == "obsoleting" }

// state is carried from the preview to Run: the exact IDs and what the
// simulation said, so Run does what the user confirmed.
type state struct {
	ids       []string
	signature string
}

func signature(cs []Change) string {
	s := make([]string, 0, len(cs))
	for _, c := range cs {
		if changing[c.Info] {
			s = append(s, c.Info+" "+c.Name+":"+c.Arch+" "+c.Version)
		}
	}
	sort.Strings(s)
	return strings.Join(s, "\n")
}

// Register adds package/install, package/remove and package/update. All
// three are previewed (a PackageKit simulation) and confirmed with the
// preview's token; removing more than the package itself needs the
// typed name.
func Register(reg *actions.Registry, cfg Config) {
	for _, op := range []Op{OpInstall, OpRemove, OpUpdate} {
		reg.Register(actions.Action{Kind: "package", Name: string(op), Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				_, _, err := Target(r.Target)
				return err
			},
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
				return plan(ctx, cfg, op, r.Target)
			},
			Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
				return run(ctx, cfg, op, p)
			}})
	}
}

// ids resolves target for op.
func ids(ctx context.Context, cfg Config, op Op, name, arch string) ([]string, error) {
	var found []Change
	var err error
	switch op {
	case OpInstall:
		found, err = cfg.PK.Resolve(ctx, false, []string{name})
	case OpRemove:
		found, err = cfg.PK.Resolve(ctx, true, []string{name})
	case OpUpdate:
		found, err = cfg.PK.Updates(ctx)
	}
	if err != nil {
		return nil, actions.Errorf(http.StatusServiceUnavailable, "PackageKit: %v", err)
	}
	var out []string
	for _, c := range found {
		if c.Name == name && (arch == "" || c.Arch == arch) && !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	if len(out) == 0 {
		switch op {
		case OpInstall:
			return nil, actions.Errorf(http.StatusNotFound, "%s is not available to install (or is installed already)", name)
		case OpRemove:
			return nil, actions.Errorf(http.StatusNotFound, "%s is not installed", name)
		default:
			return nil, actions.Errorf(http.StatusNotFound, "%s has no update", name)
		}
	}
	return out, nil
}

// protected is why removing name:arch is refused (SECURITY S16), or "".
func protected(cfg Config, name, arch string) (string, error) {
	if name == "hearth" {
		return ReasonSelf, nil
	}
	pkgs, err := cfg.Dpkg.ByName(name)
	if err != nil {
		return "", err
	}
	for _, p := range pkgs {
		if arch == "" || p.Arch == arch || p.Arch == "all" {
			if why := p.Undeletable(); why != "" {
				return undeletableReason[why], nil
			}
		}
	}
	return "", nil
}

func plan(ctx context.Context, cfg Config, op Op, target string) (actions.Plan, error) {
	name, arch, err := Target(target)
	if err != nil {
		return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%v", err)
	}
	p := actions.Plan{Summary: string(op) + " " + name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if op == OpRemove {
		// The package itself, before asking PackageKit anything.
		why, err := protected(cfg, name, arch)
		if err != nil {
			return p, actions.Errorf(http.StatusServiceUnavailable, "the package database could not be read: %v", err)
		}
		if why != "" {
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: target, Name: name}, Reason: why})
			return p, nil
		}
	}
	idList, err := ids(ctx, cfg, op, name, arch)
	if err != nil {
		return p, err
	}
	return simulated(ctx, cfg, op, name, idList, p)
}

// simulated fills p from a simulation of op on idList: every package it
// changes, blocked as a whole if it would remove a protected one.
func simulated(ctx context.Context, cfg Config, op Op, name string, idList []string, p actions.Plan) (actions.Plan, error) {
	sim, err := cfg.PK.Simulate(ctx, op, idList)
	if err != nil {
		var te *TxError
		if errors.As(err, &te) {
			return p, actions.Errorf(http.StatusConflict, "%s cannot be %s: %s", name, past(op), te.Details)
		}
		return p, actions.Errorf(http.StatusServiceUnavailable, "PackageKit: %v", err)
	}
	removed := 0
	for _, c := range sim {
		if !changing[c.Info] {
			continue
		}
		it := actions.Item{ID: c.Name + ":" + c.Arch, Name: c.Name, Detail: c.Info + " " + c.Version}
		if removes(c.Info) {
			removed++
			why, err := protected(cfg, c.Name, c.Arch)
			if err != nil {
				return p, actions.Errorf(http.StatusServiceUnavailable, "the package database could not be read: %v", err)
			}
			if why != "" {
				p.Blocked = append(p.Blocked, actions.Blocked{Item: it, Reason: why})
				continue
			}
		}
		p.Items = append(p.Items, it)
	}
	if len(p.Blocked) > 0 {
		// A transaction is all or nothing: one protected package blocks it.
		p.Items = []actions.Item{}
		return p, nil
	}
	if (op == OpRemove && removed > 1) || (op != OpRemove && removed > 0) {
		p.Confirm = name // it takes other packages with it
	}
	p.State = state{ids: idList, signature: signature(sim)}
	return p, nil
}

func past(op Op) string {
	return map[Op]string{OpInstall: "installed", OpRemove: "removed", OpUpdate: "updated"}[op]
}

func run(ctx context.Context, cfg Config, op Op, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	st, ok := p.State.(state)
	if !ok || len(p.Items) == 0 {
		return res, nil
	}
	// The transaction goes on if the browser goes away: stopping apt
	// halfway would leave the system worse off.
	ctx = context.WithoutCancel(ctx)
	sim, err := cfg.PK.Simulate(ctx, op, st.ids)
	if err != nil || signature(sim) != st.signature {
		return res, actions.Errorf(http.StatusConflict, "the packages changed since the preview; preview again")
	}
	if err := cfg.PK.Apply(ctx, op, st.ids); err != nil {
		it := actions.Item{ID: p.Items[0].ID, Name: p.Summary}
		reason, detail := ReasonError, err.Error()
		var te *TxError
		if errors.As(err, &te) {
			detail = te.Details
			if te.Code == "not-authorized" || te.Code == "transaction-cancelled" {
				reason = ReasonUnauthorized
			}
		}
		it.Detail = detail
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: reason})
		return res, nil
	}
	res.Done = len(p.Items)
	return res, nil
}
