package uninstall

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
)

// Managers an owner can have.
const (
	ManagerApt      = "apt"
	ManagerFlatpak  = "flatpak"
	ManagerSnap     = "snap"
	ManagerAppImage = "appimage"
	ManagerNpm      = "npm"
	ManagerCargo    = "cargo"
	ManagerGoBin    = "gobin"
	ManagerPip      = "pip"
	ManagerManual   = "manual"
	ManagerSteam    = "steam"
	ManagerLutris   = "lutris"
	ManagerHeroic   = "heroic"
)

// Owner is what removes the program itself.
type Owner struct {
	Manager string `json:"manager"`
	// Target names the program to its manager: "name:arch" for apt, the
	// listing's ID for the other managers, a path for an AppImage or a
	// manual install, a game ID for a launcher.
	Target string `json:"target"`
	Name   string `json:"name"`
	// Packages are apt's program packages, "name:arch".
	Packages []string `json:"packages,omitempty"`
	// Paths are an AppImage's file or a manual install's folder and files.
	Paths []string `json:"paths,omitempty"`
}

// Resolved is an item's owner and the names it is known by.
type Resolved struct {
	Item        string      `json:"item"`
	Owner       Owner       `json:"owner"`
	Identifiers Identifiers `json:"identifiers"`
}

var (
	// ErrBadItem is an item ID of no known shape.
	ErrBadItem = errors.New("not an item Hearth can uninstall")
	// ErrNotFound is an item that is not installed (any more).
	ErrNotFound = errors.New("not found")
	// ErrNoOwner is an item nothing claims and no manual install explains.
	ErrNoOwner = errors.New("no package manager or manual install owns this")
)

// Apps, Tools, Games and Packages are the discovery views the resolver
// reads (discovery.Index, Tools, Games, Dpkg).
type (
	Apps interface {
		App(id string) (discovery.App, bool)
	}
	Tools interface {
		Find(ctx context.Context, name string) (discovery.Tool, bool, error)
	}
	Games interface {
		Game(id string) (discovery.Game, bool)
		LaunchTarget(id string) (name, appID, url string, ok bool)
	}
	Packages interface {
		ByName(name string) ([]discovery.Package, error)
		Detail(id string) (discovery.PackageDetail, bool, error)
		Owners(paths []string) (map[string]string, error)
	}
)

// Resolver resolves item IDs. Extra lists another manager's packages
// (flatpak, snap, npm, cargo, gobin, pip).
type Resolver struct {
	Apps     Apps
	Tools    Tools
	Games    Games
	Packages Packages
	Extra    func(ctx context.Context, manager string) discovery.ExtraList
	Home     string
}

// Item kinds, as "<kind>:<id>".
var extraKinds = map[string]string{
	"flatpak": ManagerFlatpak, "snap": ManagerSnap, "npm": ManagerNpm, "cargo": ManagerCargo,
	"gobin": ManagerGoBin, "pip": ManagerPip,
}

// Resolve finds the owner and identifiers of item, "<kind>:<id>": app,
// tool, package, game, or one of flatpak, snap, npm, cargo, gobin, pip.
func (r *Resolver) Resolve(ctx context.Context, item string) (Resolved, error) {
	kind, id, ok := strings.Cut(item, ":")
	if !ok || id == "" {
		return Resolved{}, ErrBadItem
	}
	var res Resolved
	var err error
	switch kind {
	case "app":
		res, err = r.app(ctx, id)
	case "tool":
		res, err = r.tool(ctx, id)
	case "package":
		res, err = r.dpkg(id)
	case "game":
		res, err = r.game(ctx, id)
	default:
		m, known := extraKinds[kind]
		if !known {
			return Resolved{}, ErrBadItem
		}
		res, err = r.extra(ctx, m, id)
	}
	if err != nil {
		return Resolved{}, err
	}
	res.Item = item
	res.Identifiers = res.Identifiers.Clean()
	return res, nil
}

// program is the program name an Exec= line starts, or "".
func program(exec string) string {
	f := strings.Fields(exec)
	if len(f) == 0 {
		return ""
	}
	p := strings.Trim(f[0], `"'`)
	if p == "env" && len(f) > 1 { // "env VAR=x prog": the program follows the assignments
		for _, w := range f[1:] {
			if !strings.Contains(w, "=") {
				p = strings.Trim(w, `"'`)
				break
			}
		}
	}
	return path.Base(p)
}

func (r *Resolver) app(ctx context.Context, id string) (Resolved, error) {
	if !discovery.ValidID(id) {
		return Resolved{}, ErrBadItem
	}
	a, ok := r.Apps.App(id)
	if !ok {
		return Resolved{}, ErrNotFound
	}
	ids := Identifiers{Desktop: []string{a.ID}, WMClass: []string{a.WMClass}}
	if a.Source != discovery.SourceFlatpak {
		ids.Exec = []string{program(a.Exec)}
	}
	switch a.Source {
	case discovery.SourceFlatpak:
		ids.AppIDs = []string{a.FlatpakID}
		return r.withExtra(ctx, ManagerFlatpak, ids, func(x discovery.ExtraPackage) bool {
			return x.Kind == "app" && x.Name == a.FlatpakID
		})
	case discovery.SourceSnap:
		snap, _, _ := strings.Cut(a.ID, "_")
		ids.Snaps = []string{snap}
		return r.withExtra(ctx, ManagerSnap, ids, func(x discovery.ExtraPackage) bool { return x.Name == snap })
	case discovery.SourceAppImage:
		base := strings.TrimSuffix(path.Base(a.Path), path.Ext(a.Path))
		ids.Names = []string{base}
		return Resolved{Owner: Owner{Manager: ManagerAppImage, Target: a.Path, Name: a.Name, Paths: []string{a.Path}},
			Identifiers: ids}, nil
	}
	if reverseDNS.MatchString(a.ID) {
		ids.AppIDs = []string{a.ID}
	}
	owners, err := r.Packages.Owners([]string{a.Path})
	if err != nil {
		return Resolved{}, err
	}
	if pkg := owners[a.Path]; pkg != "" {
		res, err := r.dpkg(pkg)
		if err != nil {
			return Resolved{}, err
		}
		res.Identifiers = res.Identifiers.Merge(ids)
		return res, nil
	}
	// A user's own desktop entry: what does it start?
	prog := strings.Trim(firstField(a.Exec), `"'`)
	if strings.HasSuffix(strings.ToLower(prog), ".appimage") && filepath.IsAbs(prog) {
		ids.Names = []string{strings.TrimSuffix(path.Base(prog), path.Ext(prog))}
		return Resolved{Owner: Owner{Manager: ManagerAppImage, Target: prog, Name: a.Name, Paths: []string{prog}},
			Identifiers: ids}, nil
	}
	if filepath.IsAbs(prog) {
		if o, ok := r.manual(prog, a.Name); ok {
			ids.Names = append(ids.Names, path.Base(o.Target))
			return Resolved{Owner: o, Identifiers: ids}, nil
		}
	}
	return Resolved{}, ErrNoOwner
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// manual explains prog as a manual install: inside /opt/<name> or
// ~/.local/opt/<name>, or an unowned program in /usr/local/bin.
func (r *Resolver) manual(prog, name string) (Owner, bool) {
	prog = path.Clean(prog)
	roots := []string{"/opt"}
	if r.Home != "" {
		roots = append(roots, path.Join(r.Home, ".local/opt"), path.Join(r.Home, "Applications"))
	}
	for _, root := range roots {
		rest, ok := strings.CutPrefix(prog, root+"/")
		if !ok {
			continue
		}
		top, _, _ := strings.Cut(rest, "/")
		if top == "" || !systemNameRe.MatchString(top) {
			return Owner{}, false
		}
		dir := path.Join(root, top)
		return Owner{Manager: ManagerManual, Target: dir, Name: name, Paths: []string{dir}}, true
	}
	if path.Dir(prog) == "/usr/local/bin" && systemNameRe.MatchString(path.Base(prog)) {
		return Owner{Manager: ManagerManual, Target: prog, Name: name, Paths: []string{prog}}, true
	}
	return Owner{}, false
}

// withExtra finds the package of manager m that pick chooses.
func (r *Resolver) withExtra(ctx context.Context, m string, ids Identifiers, pick func(discovery.ExtraPackage) bool) (Resolved, error) {
	l := r.Extra(ctx, m)
	for _, x := range l.Packages {
		if pick(x) {
			ids.Packages = append(ids.Packages, x.Name)
			if m == ManagerSnap {
				ids.Snaps = append(ids.Snaps, x.Name)
			}
			if m == ManagerFlatpak {
				ids.AppIDs = append(ids.AppIDs, x.Name)
			}
			ids.Exec = append(ids.Exec, x.Bins...)
			return Resolved{Owner: Owner{Manager: m, Target: x.ID, Name: x.Name}, Identifiers: ids}, nil
		}
	}
	return Resolved{}, ErrNotFound
}

func (r *Resolver) extra(ctx context.Context, m, id string) (Resolved, error) {
	if len(id) > 4096 {
		return Resolved{}, ErrBadItem
	}
	return r.withExtra(ctx, m, Identifiers{}, func(x discovery.ExtraPackage) bool { return x.ID == id })
}

func (r *Resolver) tool(ctx context.Context, name string) (Resolved, error) {
	if !discovery.ToolNameRe.MatchString(name) {
		return Resolved{}, ErrBadItem
	}
	t, ok, err := r.Tools.Find(ctx, name)
	if err != nil {
		return Resolved{}, err
	}
	if !ok {
		return Resolved{}, ErrNotFound
	}
	ids := Identifiers{Exec: []string{t.Name}}
	switch t.Manager {
	case "apt":
		res, err := r.dpkg(t.PackageID)
		if err != nil {
			return Resolved{}, err
		}
		res.Identifiers = res.Identifiers.Merge(ids)
		return res, nil
	case "":
		for _, p := range []string{t.Target, t.Path} {
			if p == "" {
				continue
			}
			if o, ok := r.manual(p, t.Name); ok {
				ids.Names = append(ids.Names, path.Base(o.Target))
				if p == t.Target && path.Dir(t.Path) == "/usr/local/bin" {
					o.Paths = append(o.Paths, t.Path) // the link into /opt goes too
				}
				return Resolved{Owner: o, Identifiers: ids}, nil
			}
		}
		return Resolved{}, ErrNoOwner
	case "go":
		return r.extraByID(ctx, ManagerGoBin, t.PackageID, ids)
	default:
		m, known := extraKinds[t.Manager]
		if !known {
			return Resolved{}, ErrNoOwner
		}
		return r.extraByID(ctx, m, t.PackageID, ids)
	}
}

func (r *Resolver) extraByID(ctx context.Context, m, id string, ids Identifiers) (Resolved, error) {
	res, err := r.withExtra(ctx, m, ids, func(x discovery.ExtraPackage) bool { return x.ID == id })
	if errors.Is(err, ErrNotFound) {
		return Resolved{}, ErrNoOwner
	}
	return res, err
}

// programDirs are where a package's programs live.
var programDirs = map[string]bool{"/usr/bin": true, "/usr/sbin": true, "/usr/games": true, "/bin": true, "/sbin": true}

// dpkg resolves an installed Debian package, "name" or "name:arch".
func (r *Resolver) dpkg(target string) (Resolved, error) {
	name, arch, err := pkgs.Target(target)
	if err != nil {
		return Resolved{}, ErrBadItem
	}
	list, err := r.Packages.ByName(name)
	if err != nil {
		return Resolved{}, err
	}
	o := Owner{Manager: ManagerApt, Target: target, Name: name}
	ids := Identifiers{Packages: []string{name}}
	for _, p := range list {
		if p.State != "installed" || (arch != "" && p.Arch != arch) {
			continue
		}
		o.Packages = append(o.Packages, p.ID())
		det, ok, err := r.Packages.Detail(p.ID())
		if err != nil {
			return Resolved{}, err
		}
		if !ok {
			continue
		}
		for _, f := range det.Files {
			dir, base := path.Split(f)
			dir = strings.TrimSuffix(dir, "/")
			switch {
			case dir == "/usr/share/applications" && strings.HasSuffix(base, ".desktop"):
				id := strings.TrimSuffix(base, ".desktop")
				ids.Desktop = append(ids.Desktop, id)
				if a, ok := r.Apps.App(id); ok {
					ids.WMClass = append(ids.WMClass, a.WMClass)
				}
				if reverseDNS.MatchString(id) {
					ids.AppIDs = append(ids.AppIDs, id)
				}
			case programDirs[dir]:
				ids.Exec = append(ids.Exec, base)
			}
		}
	}
	if len(o.Packages) == 0 {
		return Resolved{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return Resolved{Owner: o, Identifiers: ids}, nil
}

func (r *Resolver) game(ctx context.Context, id string) (Resolved, error) {
	if len(id) > 255 {
		return Resolved{}, ErrBadItem
	}
	g, ok := r.Games.Game(id)
	if !ok {
		return Resolved{}, ErrNotFound
	}
	native := strings.TrimPrefix(id, g.Source+".")
	switch g.Source {
	case discovery.GameSteam:
		// Its leftovers are found by app ID under steamapps, never by
		// guessing: a number names no folder of its own elsewhere.
		return Resolved{Owner: Owner{Manager: ManagerSteam, Target: native, Name: g.Name}}, nil
	case discovery.GameLutris:
		return Resolved{Owner: Owner{Manager: ManagerLutris, Target: id, Name: g.Name}}, nil
	case discovery.GameHeroic:
		return Resolved{Owner: Owner{Manager: ManagerHeroic, Target: id, Name: g.Name}}, nil
	}
	_, appID, _, _ := r.Games.LaunchTarget(id)
	if appID == "" {
		return Resolved{}, ErrNoOwner
	}
	return r.app(ctx, appID)
}
