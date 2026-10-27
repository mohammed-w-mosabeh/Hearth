package pkgs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Removal of packages from managers other than dpkg (ROADMAP 3.2). Each
// remove is previewed (the item, or why it is blocked) and confirmed with
// the token. Snaps go through snapd, which asks polkit; the others run
// the manager's own uninstall (or gio trash for a Go binary) through
// UserRunner. pip --user packages are listed only.

// ExtraConfig configures the extra managers' actions.
type ExtraConfig struct {
	Find discovery.ExtraConfig
	Run  *UserRunner
	// SnapPoll is how often a snap removal is checked; default 1 s.
	SnapPoll time.Duration
}

type extraKind struct {
	kind  string
	list  func(ctx context.Context, c discovery.ExtraConfig) discovery.ExtraList
	valid func(target string) error
}

var extraKinds = []extraKind{
	{"flatpak", func(_ context.Context, c discovery.ExtraConfig) discovery.ExtraList { return discovery.Flatpak(c) }, validFlatpak},
	{"snap", discovery.Snap, matches(discovery.SnapNameRe, "a snap name")},
	{"npm", func(_ context.Context, c discovery.ExtraConfig) discovery.ExtraList { return discovery.Npm(c) }, validNpm},
	{"cargo", func(_ context.Context, c discovery.ExtraConfig) discovery.ExtraList { return discovery.Cargo(c) },
		matches(discovery.CrateNameRe, "a crate name")},
	{"gobin", func(_ context.Context, c discovery.ExtraConfig) discovery.ExtraList { return discovery.GoBinaries(c) },
		matches(discovery.GoBinNameRe, "a program name")},
}

func matches(re interface{ MatchString(string) bool }, what string) func(string) error {
	return func(s string) error {
		if !re.MatchString(s) {
			return fmt.Errorf("target must be %s", what)
		}
		return nil
	}
}

// validFlatpak checks "<system|user>/<app|runtime>/<id>/<arch>/<branch>".
func validFlatpak(s string) error {
	f := strings.Split(s, "/")
	if len(f) != 5 || (f[0] != "system" && f[0] != "user") || (f[1] != "app" && f[1] != "runtime") {
		return errors.New(`target must be "<system|user>/<app|runtime>/<id>/<arch>/<branch>"`)
	}
	for _, p := range f[2:] {
		if !discovery.FlatpakRefRe.MatchString(p) {
			return errors.New("bad flatpak ref")
		}
	}
	return nil
}

// validNpm checks "<prefix>::<name>".
func validNpm(s string) error {
	prefix, name, ok := strings.Cut(s, "::")
	if !ok || !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix || !discovery.NpmNameRe.MatchString(name) {
		return errors.New(`target must be "<prefix>::<package>"`)
	}
	return nil
}

// RegisterExtra adds <kind>/remove for flatpak, snap, npm, cargo and gobin.
func RegisterExtra(reg *actions.Registry, cfg ExtraConfig) {
	for _, k := range extraKinds {
		reg.Register(actions.Action{Kind: k.kind, Name: "remove", Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				return k.valid(r.Target)
			},
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
				return extraPlan(ctx, cfg, k, r.Target)
			},
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return extraRun(ctx, cfg, k.kind, p)
			}})
	}
}

func extraPlan(ctx context.Context, cfg ExtraConfig, k extraKind, target string) (actions.Plan, error) {
	l := k.list(ctx, cfg.Find)
	p := actions.Plan{Summary: "remove " + target, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	for _, x := range l.Packages {
		if x.ID != target {
			continue
		}
		p.Summary = "remove " + x.Name
		it := actions.Item{ID: x.ID, Name: x.Name, Detail: x.Version}
		if !x.Removable {
			if len(x.UsedBy) > 0 {
				it.Detail = strings.Join(x.UsedBy, ", ")
			}
			p.Blocked = append(p.Blocked, actions.Blocked{Item: it, Reason: x.Why})
			return p, nil
		}
		p.Items = append(p.Items, it)
		p.State = x // what was shown is what is removed
		return p, nil
	}
	if !l.Available {
		return p, actions.Errorf(http.StatusServiceUnavailable, "%s is not installed", l.Manager)
	}
	return p, actions.Errorf(http.StatusNotFound, "%s is not installed", target)
}

func extraRun(ctx context.Context, cfg ExtraConfig, kind string, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	x, ok := p.State.(discovery.ExtraPackage)
	if !ok || len(p.Items) == 0 {
		return res, nil
	}
	// As with apt: the removal goes on if the browser goes away.
	ctx = context.WithoutCancel(ctx)
	var err error
	switch kind {
	case "snap":
		err = removeSnap(ctx, cfg, x.Name)
	default:
		path, argv, env := command(cfg.Find, kind, x)
		_, err = cfg.Run.Run(ctx, "Remove "+x.Name, path, argv, env)
	}
	if err != nil {
		it := p.Items[0]
		it.Detail = err.Error()
		reason := ReasonError
		if d := strings.ToLower(it.Detail); strings.Contains(d, "not authorized") || strings.Contains(d, "not allowed for user") || strings.Contains(d, "access denied") {
			reason = ReasonUnauthorized
		}
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: reason})
		return res, nil
	}
	res.Done = 1
	return res, nil
}

// command is the fixed uninstall command for x; arguments come from the
// listing, which validated them.
func command(c discovery.ExtraConfig, kind string, x discovery.ExtraPackage) (path string, argv, env []string) {
	basePath := "/usr/local/bin:/usr/bin:/bin"
	home := "HOME=" + c.Home
	switch kind {
	case "flatpak":
		f := strings.Split(x.ID, "/") // installation/kind/id/arch/branch
		return "/usr/bin/flatpak", []string{"flatpak", "uninstall", "-y", "--" + f[0], strings.Join(f[1:], "/")},
			[]string{home, "PATH=" + basePath}
	case "npm":
		prefix := x.Location
		bin := filepath.Join(prefix, "bin")
		// npm is "#!/usr/bin/env node": its own node must come first.
		return filepath.Join(bin, "npm"), []string{"npm", "uninstall", "--global", x.Name},
			[]string{home, "PATH=" + bin + ":" + basePath, "NPM_CONFIG_PREFIX=" + prefix}
	case "cargo":
		bin := filepath.Join(x.Location, "bin")
		return filepath.Join(bin, "cargo"), []string{"cargo", "uninstall", x.Name},
			[]string{home, "PATH=" + bin + ":" + basePath, "CARGO_HOME=" + x.Location}
	case "gobin":
		// Into the trash, not deleted (SECURITY S17).
		return "/usr/bin/gio", []string{"gio", "trash", "--", x.Location}, []string{home, "PATH=" + basePath}
	}
	return "", nil, nil
}

// removeSnap asks snapd to remove name (it asks polkit, interactively)
// and follows the change to its end.
func removeSnap(ctx context.Context, cfg ExtraConfig, name string) error {
	var r struct {
		Change string `json:"change"`
	}
	if err := discovery.SnapdJSON(ctx, cfg.Find.SnapSocket, http.MethodPost, "/v2/snaps/"+name,
		strings.NewReader(`{"action":"remove"}`), &r, "X-Allow-Interaction", "true"); err != nil {
		return err
	}
	if r.Change == "" || strings.ContainsAny(r.Change, "/?#") {
		return errors.New("snapd did not start a change")
	}
	poll := cfg.SnapPoll
	if poll == 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		var c struct {
			Result struct {
				Ready  bool   `json:"ready"`
				Status string `json:"status"`
				Err    string `json:"err"`
			} `json:"result"`
		}
		if err := discovery.SnapdJSON(ctx, cfg.Find.SnapSocket, http.MethodGet, "/v2/changes/"+r.Change, nil, &c); err != nil {
			return err
		}
		if c.Result.Ready {
			if c.Result.Status != "Done" {
				return errors.New(c.Result.Err)
			}
			return nil
		}
		time.Sleep(poll)
	}
	return errors.New("snapd did not finish in 30 minutes")
}
