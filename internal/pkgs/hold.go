package pkgs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Hold, update all, and the list of updates (ROADMAP 3.3).

// Broker is the subset of broker.Client the hold actions need.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Reasons specific to hold.
const (
	ReasonNotInstalled = "not-found" // the package is not installed
)

// RegisterHold adds package/hold and package/unhold (reversible: the UI
// offers the other as Undo) and package/update-all. Hold goes through
// hearth-broker (pkg.hold), which asks polkit.
func RegisterHold(reg *actions.Registry, cfg Config, b Broker) {
	for _, hold := range []bool{true, false} {
		name := "unhold"
		if hold {
			name = "hold"
		}
		reg.Register(actions.Action{Kind: "package", Name: name, Risk: actions.Reversible,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				_, _, err := Target(r.Target)
				return err
			},
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
				return holdPlan(cfg, hold, r.Target)
			},
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return holdRun(ctx, b, hold, p)
			}})
	}
	reg.Register(actions.Action{Kind: "package", Name: "update-all", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 || r.Target != "all" {
				return errors.New(`target must be "all" and params empty`)
			}
			return nil
		},
		Plan: func(ctx context.Context, _ actions.Request) (actions.Plan, error) { return updateAllPlan(ctx, cfg) },
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			return run(ctx, cfg, OpUpdate, p)
		}})
}

func holdPlan(cfg Config, hold bool, target string) (actions.Plan, error) {
	name, arch, _ := Target(target)
	verb := map[bool]string{true: "hold ", false: "unhold "}[hold]
	p := actions.Plan{Summary: verb + name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	pkgs, err := cfg.Dpkg.ByName(name)
	if err != nil {
		return p, actions.Errorf(http.StatusServiceUnavailable, "the package database could not be read: %v", err)
	}
	for _, x := range pkgs {
		if (arch == "" || x.Arch == arch) && x.State == "installed" {
			if x.Hold == hold {
				return p, actions.Errorf(http.StatusConflict, "%s is %s already", name, map[bool]string{true: "held", false: "not held"}[hold])
			}
			p.Items = append(p.Items, actions.Item{ID: target, Name: name, Detail: x.Version})
			return p, nil
		}
	}
	return p, actions.Errorf(http.StatusNotFound, "%s is not installed", target)
}

func holdRun(ctx context.Context, b Broker, hold bool, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
	if len(p.Items) == 0 {
		return res, nil
	}
	it := p.Items[0]
	err := b.Call(ctx, "pkg.hold", broker.HoldParams{Package: it.ID, Hold: hold}, nil)
	var be *broker.Error
	switch {
	case err == nil:
		res.Done = 1
	case errors.Is(err, broker.ErrUnauthorized):
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonUnauthorized})
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonNotInstalled})
	case errors.As(err, &be) && be.Code == broker.CodeFailed:
		it.Detail = be.Message
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
	case errors.Is(err, broker.ErrUnavailable):
		return res, actions.Errorf(http.StatusServiceUnavailable, "hearth-broker is not available")
	case errors.Is(err, broker.ErrNotMember):
		return res, actions.Errorf(http.StatusForbidden, "you are not in the hearth group")
	default:
		return res, actions.Errorf(http.StatusBadGateway, "broker: %v", err)
	}
	return res, nil
}

// updatable are the infos of an update that can be applied ("blocked" is
// a held package, or one whose dependencies cannot be met).
func updatable(info string) bool {
	return info != "blocked" && info != "installed" && info != "available"
}

func updateAllPlan(ctx context.Context, cfg Config) (actions.Plan, error) {
	p := actions.Plan{Summary: "update all", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	ups, err := cfg.PK.Updates(ctx)
	if err != nil {
		return p, actions.Errorf(http.StatusServiceUnavailable, "PackageKit: %v", err)
	}
	var idList []string
	for _, u := range ups {
		if updatable(u.Info) {
			idList = append(idList, u.ID)
		}
	}
	if len(idList) == 0 {
		return p, actions.Errorf(http.StatusNotFound, "everything is up to date")
	}
	return simulated(ctx, cfg, OpUpdate, "all", idList, p)
}

// Update is one available update.
type Update struct {
	Name     string `json:"name"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`  // the new version
	Severity string `json:"severity"` // PackageKit's info: security, important, bugfix, normal, low, blocked, ...
	Security bool   `json:"security"`
	Summary  string `json:"summary,omitempty"`
}

// Updates serves the available updates, cached until the package
// database changes (Stamp) or for TTL.
type Updates struct {
	PK    *Client
	Stamp func() string
	TTL   time.Duration // default 10 min

	mu    sync.Mutex
	list  []Update
	stamp string
	at    time.Time
}

// List returns the updates, sorted security first, then by name.
func (u *Updates) List(ctx context.Context) ([]Update, error) {
	ttl := u.TTL
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	stamp := ""
	if u.Stamp != nil {
		stamp = u.Stamp()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.list != nil && stamp == u.stamp && time.Since(u.at) < ttl {
		return u.list, nil
	}
	cs, err := u.PK.Updates(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]Update, 0, len(cs))
	seen := map[string]bool{}
	for _, c := range cs {
		if seen[c.Name+":"+c.Arch] || c.Info == "installed" || c.Info == "available" {
			continue
		}
		seen[c.Name+":"+c.Arch] = true
		sev := c.Severity
		if sev == "" || c.Info == "blocked" {
			sev = c.Info
		}
		list = append(list, Update{Name: c.Name, Arch: c.Arch, Version: c.Version, Severity: sev,
			Security: sev == "security" || sev == "critical", Summary: c.Summary})
	}
	sortUpdates(list)
	u.list, u.stamp, u.at = list, stamp, time.Now()
	return list, nil
}

func sortUpdates(l []Update) {
	sort.Slice(l, func(i, j int) bool {
		if l[i].Security != l[j].Security {
			return l[i].Security
		}
		if l[i].Name != l[j].Name {
			return l[i].Name < l[j].Name
		}
		return l[i].Arch < l[j].Arch
	})
}

// HandleList serves GET /api/v1/pkgs/updates.
func (u *Updates) HandleList(w http.ResponseWriter, r *http.Request) {
	list, err := u.List(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not available") || strings.Contains(msg, "system bus") {
			http.Error(w, "PackageKit is not available", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "the updates could not be listed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"updates": list})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
