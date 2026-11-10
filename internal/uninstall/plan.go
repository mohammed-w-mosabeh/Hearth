package uninstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
)

// Plan is one item's complete preview (docs/plan/uninstall.md §2, §7).
type Plan struct {
	Item       string      `json:"item"`
	Name       string      `json:"name"`
	Owner      Owner       `json:"owner"`
	Candidates []Candidate `json:"candidates"`
	// Blocked, when set, is why the plan cannot run at all: a protected
	// package in the removal, or a process that cannot be stopped.
	Blocked string `json:"blocked,omitempty"`
	// Desktop are packages in the removal that can end the session; their
	// removal needs DesktopWords typed.
	Desktop []string `json:"desktop,omitempty"`
	// Freed is the space the preselected rows free, in bytes.
	Freed int64 `json:"freed"`
}

// DesktopWords must be typed to remove a desktop or display package.
const DesktopWords = "remove my desktop"

// Key is a candidate's key in a selection: its group and ID.
func (c Candidate) Key() string { return c.Group + "|" + c.ID }

// PackageKit is what the builder and the engine ask PackageKit.
type PackageKit interface {
	Resolve(ctx context.Context, installed bool, names []string) ([]pkgs.Change, error)
	Simulate(ctx context.Context, op pkgs.Op, ids []string) ([]pkgs.Change, error)
	Apply(ctx context.Context, op pkgs.Op, ids []string) error
}

// Builder builds plans.
type Builder struct {
	Resolver *Resolver
	Local    Local
	Running  Running
	Scan     Scan
	PK       PackageKit
	// Simulate runs apt-get-simulate with names (safeexec).
	Simulate func(ctx context.Context, names ...string) (string, error)
	// Installed is dpkg's view; Release the running kernel's.
	Installed func() ([]discovery.Package, error)
	Release   func() string
	// Session lists what runs the graphical session (SessionPrograms).
	Session func() []string
	// Apps lists every desktop app, for the names that stay installed.
	Apps  func() []discovery.App
	Extra func(ctx context.Context, manager string) discovery.ExtraList
	Refs  func(ctx context.Context) (RefData, error)
	// Dpkg reads dpkg's file lists as the user (archive.Roots{} in
	// production: they are world-readable).
	Dpkg archive.Roots
}

// ids lists the package IDs PackageKit names for "name:arch" ids.
func resolveIDs(ctx context.Context, pk PackageKit, want []string) ([]string, error) {
	var names []string
	for _, id := range want {
		n, _, _ := strings.Cut(id, ":")
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	found, err := pk.Resolve(ctx, true, names)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range found {
		if slices.Contains(want, c.Name+":"+c.Arch) && !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("PackageKit does not know %s", strings.Join(want, ", "))
	}
	return out, nil
}

// removedBy lists what a PackageKit simulation removes, as "name:arch".
func removedBy(sim []pkgs.Change) []string {
	var out []string
	for _, c := range sim {
		if c.Info == "removing" || c.Info == "obsoleting" {
			out = append(out, c.Name+":"+c.Arch)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Build makes item's plan.
func (b *Builder) Build(ctx context.Context, item string) (Plan, error) {
	res, err := b.Resolver.Resolve(ctx, item)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Item: item, Name: res.Owner.Name, Owner: res.Owner}
	program := b.Local.Program(res)
	installed := []discovery.Package{}
	if b.Installed != nil {
		if installed, err = b.Installed(); err != nil {
			return p, fmt.Errorf("the package database could not be read: %w", err)
		}
	}
	guard := Guard{Installed: installed}
	if b.Release != nil {
		guard.Running = b.Release()
	}
	size := map[string]int64{}
	for _, pk := range installed {
		size[pk.ID()] = pk.InstalledKiB << 10
	}
	var removals, orphans []string
	if res.Owner.Manager == ManagerApt {
		// The program's own packages first: a protected one blocks the plan
		// before PackageKit is asked anything (it may refuse to simulate).
		for _, id := range res.Owner.Packages {
			if why := guard.Blocked(id); why != "" {
				p.Blocked = fmt.Sprintf("%s can never be removed (%s)", id, why)
				p.Candidates = program
				return p, nil
			}
		}
		var kept []Kept
		removals, orphans, kept, err = b.packages(ctx, res, installed)
		if err != nil {
			return p, err
		}
		for _, id := range removals {
			if !slices.Contains(res.Owner.Packages, id) {
				program = append(program, Candidate{Group: GroupProgram, Kind: KindPackage, ID: id, Name: id, Preselect: true,
					Reason: "depends on it, so it goes with it"})
			}
		}
		for _, id := range orphans {
			program = append(program, Candidate{Group: GroupOrphans, Kind: KindPackage, ID: id, Name: id, Preselect: true,
				Reason: "installed only for it; nothing else needs it any more"})
		}
		for _, k := range kept {
			program = append(program, remains(k.ID, k.ID, "installed by you, so it stays"))
		}
		for _, id := range append(slices.Clone(removals), orphans...) {
			program = append(program, Candidate{Group: GroupPurge, Kind: KindPackage, ID: id, Name: id, Preselect: true,
				Reason: "its configuration files, copied first so they can be restored"})
		}
		session := map[string]bool{}
		if b.Session != nil {
			session = b.sessionPackages()
		}
		for _, id := range append(slices.Clone(removals), orphans...) {
			if why := guard.Blocked(id); why != "" && slices.Contains(removals, id) {
				p.Blocked = fmt.Sprintf("%s can never be removed (%s)", id, why)
			}
			if guard.Desktop(id, session) && slices.Contains(removals, id) {
				p.Desktop = append(p.Desktop, id)
			}
		}
	}
	for i := range program {
		if program[i].Kind == KindPackage && program[i].Group != GroupPurge {
			program[i].Size = size[program[i].ID]
		}
	}
	if res.Owner.Manager == ManagerFlatpak && b.Extra != nil {
		program = append(program, FlatpakFreed(b.Extra(ctx, ManagerFlatpak), res.Owner.Target)...)
	}
	if res.Owner.Manager == ManagerAppImage {
		for _, path := range res.Owner.Paths {
			program = append(program, b.Local.AppImageIntegration(path)...)
		}
	}
	if res.Owner.Manager == ManagerSteam {
		program = append(program, b.Local.SteamLeftovers(res.Owner.Target)...)
	}
	p.Candidates = program
	p.Candidates = append(p.Candidates, b.Running.Parts(res, program)...)
	skip := map[string]bool{}
	for _, c := range p.Candidates {
		skip[c.ID] = true
	}
	scan := b.Scan
	scan.Others = b.others(ctx, res, append(slices.Clone(removals), orphans...), installed)
	p.Candidates = append(p.Candidates, scan.UserLeftovers(ctx, res.Identifiers, skip)...)
	p.Candidates = append(p.Candidates, b.systemLeftovers(ctx, res.Identifiers, skip)...)
	if b.Refs != nil {
		if d, err := b.Refs(ctx); err == nil {
			var units []string
			for _, c := range p.Candidates {
				if c.Kind == KindUnit {
					units = append(units, c.ID)
				}
			}
			p.Candidates = append(p.Candidates, Refs(d, res, units)...)
		}
	}
	for _, c := range p.Candidates {
		if c.Warn == "blocks" && p.Blocked == "" {
			p.Blocked = c.Name + ": " + c.Reason
		}
		if c.Preselect && c.Group != GroupPurge {
			p.Freed += c.Size
		}
	}
	return p, nil
}

// packages simulates the apt removal: what it takes along, what it
// orphans, and the orphans left because the user installed them.
func (b *Builder) packages(ctx context.Context, res Resolved, installed []discovery.Package) (removals, orphans []string, kept []Kept, err error) {
	if b.PK == nil {
		return nil, nil, nil, errors.New("PackageKit is not available")
	}
	ids, err := resolveIDs(ctx, b.PK, res.Owner.Packages)
	if err != nil {
		return nil, nil, nil, err
	}
	sim, err := b.PK.Simulate(ctx, pkgs.OpRemove, ids)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the removal could not be simulated: %w", err)
	}
	removals = removedBy(sim)
	if b.Simulate == nil {
		return removals, nil, nil, nil
	}
	beforeOut, err := b.Simulate(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("apt could not simulate: %w", err)
	}
	afterOut, err := b.Simulate(ctx, res.Owner.Packages...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("apt could not simulate: %w", err)
	}
	before, err := ParseSim(beforeOut)
	if err != nil {
		return nil, nil, nil, err
	}
	after, err := ParseSim(afterOut)
	if err != nil {
		return nil, nil, nil, err
	}
	var taken []string
	for _, id := range removals {
		if !slices.Contains(res.Owner.Packages, id) {
			taken = append(taken, id)
		}
	}
	orphans, kept = Orphans(before, after, res.Owner.Packages, taken, installed, NativeArch())
	g := Guard{Installed: installed}
	if b.Release != nil {
		g.Running = b.Release()
	}
	orphans = slices.DeleteFunc(orphans, func(id string) bool { return g.Blocked(id) != "" })
	return removals, orphans, kept, nil
}

// sessionPackages are the packages owning what runs the session.
func (b *Builder) sessionPackages() map[string]bool {
	out := map[string]bool{}
	progs := b.Session()
	if len(progs) == 0 {
		return out
	}
	owned, err := archive.OwnedUnder(b.Dpkg, progs)
	if err != nil {
		return out
	}
	for _, pkg := range owned {
		n, _, _ := strings.Cut(pkg, ":")
		out[n] = true
	}
	return out
}

// others are the names of everything that stays installed.
func (b *Builder) others(ctx context.Context, res Resolved, going []string, installed []discovery.Package) Identifiers {
	var o Identifiers
	if b.Apps != nil {
		for _, a := range b.Apps() {
			if slices.Contains(res.Identifiers.Desktop, a.ID) || slices.Contains(res.Identifiers.AppIDs, a.ID) ||
				(a.FlatpakID != "" && slices.Contains(res.Identifiers.AppIDs, a.FlatpakID)) {
				continue // the item's own entries
			}
			o.Desktop = append(o.Desktop, a.ID)
			o.WMClass = append(o.WMClass, a.WMClass)
			o.AppIDs = append(o.AppIDs, a.FlatpakID)
			o.Exec = append(o.Exec, program(a.Exec))
		}
	}
	for _, pk := range installed {
		if pk.State == "installed" && !slices.Contains(going, pk.ID()) && !slices.Contains(res.Identifiers.Packages, pk.Name) {
			o.Packages = append(o.Packages, pk.Name)
		}
	}
	if b.Extra != nil {
		for _, m := range []string{ManagerFlatpak, ManagerSnap} {
			for _, x := range b.Extra(ctx, m).Packages {
				if x.ID == res.Owner.Target {
					continue
				}
				if m == ManagerSnap {
					o.Snaps = append(o.Snaps, x.Name)
				} else if x.Kind == "app" {
					o.AppIDs = append(o.AppIDs, x.Name)
				}
			}
		}
	}
	return o
}

// dataNames are folders that hold people's data, whatever installed them.
var dataNames = set("mysql", "mariadb", "postgresql", "docker", "containerd", "mail", "vmail", "www", "redis", "mongodb",
	"couchdb", "gitea", "forgejo", "nextcloud")

// dataFiles are names that mark a database.
var dataFiles = []string{".db", ".sqlite", ".sqlite3", "ibdata1", "PG_VERSION", "Maildir"}

const dataBytes = 100 << 20

// looksLikeData says whether p seems to hold a database or mail: by its
// name, its size, or a database file two levels down at most (as far as
// the user can read).
func looksLikeData(p string, size int64) bool {
	if dataNames[strings.ToLower(filepath.Base(p))] || size >= dataBytes {
		return true
	}
	found := false
	base := strings.Count(p, "/")
	_ = filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() && strings.Count(q, "/")-base >= 2 {
			return filepath.SkipDir
		}
		for _, f := range dataFiles {
			if strings.HasSuffix(d.Name(), f) || d.Name() == f {
				found = true
			}
		}
		return nil
	})
	return found
}

// systemLeftovers are exact-name children of the system roots that no
// package owns, in a folder only root can write; never preselected.
func (b *Builder) systemLeftovers(ctx context.Context, ids Identifiers, skip map[string]bool) []Candidate {
	var names []string
	for _, n := range slices.Concat(ids.Packages, ids.Exec, ids.Names) {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	var out []Candidate
	var paths []string
	matches := map[string]Match{}
	for _, root := range SystemRoots {
		for _, n := range names {
			p := root + "/" + n
			if skip[p] {
				continue
			}
			if _, _, err := CheckSystem(p); err != nil {
				continue
			}
			m, ok := ids.MatchSystem(root, n)
			if !ok {
				continue
			}
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				out = append(out, remains(p, p, "a link, left alone"))
				continue
			case root == "/usr/local/bin" && !fi.Mode().IsRegular():
				continue
			}
			_, safe, err := owner(p)
			if err != nil {
				continue
			}
			if !safe {
				out = append(out, remains(p, p, "its folder can be written by someone other than root"))
				continue
			}
			matches[p] = m
			paths = append(paths, p)
		}
	}
	owned, err := archive.OwnedUnder(b.Dpkg, paths)
	if err != nil {
		return append(out, remains("dpkg", "system leftovers", "dpkg's file lists could not be read"))
	}
	for _, p := range paths {
		if pkg := owned[p]; pkg != "" {
			out = append(out, remains(p, p, "belongs to the installed package "+pkg))
			continue
		}
		c, ok := pathCandidate(GroupSystem, KindArchive, p, matches[p].Reason+"; archived for 7 days", false)
		if !ok {
			continue
		}
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			c.Size = measure(ctx, p)
		}
		if looksLikeData(p, c.Size) {
			c.Warn = "data"
		}
		out = append(out, c)
	}
	return out
}
