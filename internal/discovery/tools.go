package discovery

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Tools (SPEC 3, ROADMAP 3.4): every program in PATH, with what owns it,
// its version and a one-line description. Nothing in PATH is ever run:
// the owner comes from dpkg's file lists or another manager's listing,
// the version from the owner (or a Go binary's build info), and the
// description from the man page index (one allowlisted apropos).

// Tool is one program found in PATH.
type Tool struct {
	Name   string `json:"name"`
	Path   string `json:"path"`             // where PATH finds it
	Target string `json:"target,omitempty"` // what a symlink resolves to
	// Alternative: the link goes through /etc/alternatives.
	Alternative bool `json:"alternative,omitempty"`
	// Manager is what installed it: apt, flatpak, snap, npm, cargo, go,
	// or "" when nothing claims it.
	Manager   string `json:"manager,omitempty"`
	Package   string `json:"package,omitempty"`    // the owner's name
	PackageID string `json:"package_id,omitempty"` // the owner as an action target
	Version   string `json:"version,omitempty"`
	Summary   string `json:"summary,omitempty"` // from its man page
	// Shadowed are later PATH entries of the same name, never run.
	Shadowed []string `json:"shadowed,omitempty"`
}

// ToolNameRe is a program name Hearth lists (and asks man about).
var ToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,127}$`)

// maxTools bounds one listing.
const maxTools = 20000

// ToolsConfig configures the tools lister.
type ToolsConfig struct {
	Extra ExtraConfig
	Dpkg  *Dpkg
	// Path is hearthd's PATH; the usual user and manager bin directories
	// are added (the user manager's PATH is shorter than a shell's).
	Path []string
	// System are directories searched after the user's; default
	// /usr/local/sbin, /usr/sbin, /sbin, /snap/bin.
	System []string
	Run    Runner // runs apropos and man (safeexec.Run in production)
	Log    *slog.Logger
}

// Runner runs an allowlisted command.
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Tools lists the tools, caching the list until a PATH directory, dpkg's
// database or the man index changes.
type Tools struct {
	cfg ToolsConfig

	mu     sync.Mutex
	cached []Tool
	stamp  string
}

// NewTools returns a Tools.
func NewTools(cfg ToolsConfig) *Tools {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Tools{cfg: cfg}
}

// Dirs are the directories searched, in order, without duplicates (a
// directory and a symlink to it, such as /bin and /usr/bin, count once).
func (t *Tools) Dirs() []string {
	c := t.cfg.Extra
	cands := append([]string{}, t.cfg.Path...)
	cands = append(cands,
		filepath.Join(c.Home, ".local/bin"), filepath.Join(c.Home, "bin"), GoBin(c), filepath.Join(CargoHome(c), "bin"))
	for _, p := range NpmPrefixes(c) {
		if strings.HasPrefix(p, c.Home+"/") {
			cands = append(cands, filepath.Join(p, "bin"))
		}
	}
	system := t.cfg.System
	if system == nil {
		system = []string{"/usr/local/sbin", "/usr/sbin", "/sbin", "/snap/bin"}
	}
	cands = append(cands, system...)
	cands = append(cands, filepath.Join(c.dataHome(), "flatpak/exports/bin"))
	for _, r := range flatpakRoots(c) {
		cands = append(cands, filepath.Join(r, "exports/bin"))
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range cands {
		if !filepath.IsAbs(d) {
			continue
		}
		canon, err := filepath.EvalSymlinks(d)
		if err != nil || seen[canon] {
			continue
		}
		if fi, err := os.Stat(canon); err != nil || !fi.IsDir() {
			continue
		}
		seen[canon] = true
		out = append(out, filepath.Clean(d))
	}
	return out
}

func (t *Tools) stampOf(dirs []string) string {
	paths := append([]string{t.cfg.Dpkg.cfg.Status, "/var/cache/man/index.db"}, dirs...)
	return stampOf(paths...)
}

// List returns the tools, sorted by name. The result is shared: callers
// must not change it.
func (t *Tools) List(ctx context.Context) ([]Tool, error) {
	dirs := t.Dirs()
	stamp := t.stampOf(dirs)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached != nil && stamp == t.stamp {
		return t.cached, nil
	}
	tools := scanDirs(dirs)
	if err := t.owners(tools); err != nil {
		return nil, err
	}
	t.otherOwners(ctx, tools)
	t.summaries(ctx, tools)
	t.cached, t.stamp = tools, stamp
	return tools, nil
}

// Find returns the tool named name, if listed.
func (t *Tools) Find(ctx context.Context, name string) (Tool, bool, error) {
	tools, err := t.List(ctx)
	if err != nil {
		return Tool{}, false, err
	}
	i := sort.Search(len(tools), func(i int) bool { return tools[i].Name >= name })
	if i < len(tools) && tools[i].Name == name {
		return tools[i], true, nil
	}
	return Tool{}, false, nil
}

// scanDirs finds the executables in dirs; the first of a name wins, as in
// a shell.
func scanDirs(dirs []string) []Tool {
	byName := map[string]int{}
	var tools []Tool
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			name := e.Name()
			if !ToolNameRe.MatchString(name) || len(tools) >= maxTools {
				continue
			}
			p := filepath.Join(d, name)
			fi, err := os.Stat(p) // follows links
			if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
				continue
			}
			if i, ok := byName[name]; ok {
				tools[i].Shadowed = append(tools[i].Shadowed, p)
				continue
			}
			tl := Tool{Name: name, Path: p}
			if e.Type()&os.ModeSymlink != 0 {
				if target, err := filepath.EvalSymlinks(p); err == nil {
					tl.Target = target
				}
				if first, err := os.Readlink(p); err == nil && strings.Contains(first, "/etc/alternatives/") {
					tl.Alternative = true
				}
			}
			byName[name] = len(tools)
			tools = append(tools, tl)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// canonical is p with its directory's symlinks resolved (/bin/ls is
// /usr/bin/ls on a merged-/usr system), the file itself left alone.
func canonical(p string, cache map[string]string) string {
	dir, base := path.Split(p)
	c, ok := cache[dir]
	if !ok {
		c = dir
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			c = r + "/"
		}
		cache[dir] = c
	}
	return c + base
}

// owners fills the dpkg owner of each tool (of the link itself, else of
// what it resolves to) from dpkg's file lists, info/<package>.list.
func (t *Tools) owners(tools []Tool) error {
	cache := map[string]string{}
	want := map[string]bool{}
	bases := map[string]bool{}
	for _, tl := range tools {
		for _, p := range []string{tl.Path, tl.Target} {
			if p != "" {
				want[canonical(p, cache)] = true
				bases[path.Base(p)] = true
			}
		}
	}
	info := filepath.Join(filepath.Dir(t.cfg.Dpkg.cfg.Status), "info")
	lists, err := os.ReadDir(info)
	if err != nil {
		return err
	}
	owner := map[string]string{}
	for _, l := range lists {
		pkg, ok := strings.CutSuffix(l.Name(), ".list")
		if !ok {
			continue
		}
		f, err := os.Open(filepath.Join(info, l.Name())) // #nosec G304 -- dpkg's own info directory
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			line := sc.Text()
			if !bases[path.Base(line)] {
				continue
			}
			if c := canonical(line, cache); want[c] && owner[c] == "" {
				owner[c] = pkg
			}
		}
		_ = f.Close()
	}
	pkgs, err := t.cfg.Dpkg.Packages()
	if err != nil {
		return err
	}
	version := map[string]string{}
	for _, p := range pkgs {
		version[p.ID()] = p.Version
		if _, ok := version[p.Name]; !ok {
			version[p.Name] = p.Version
		}
	}
	for i, tl := range tools {
		pkg := owner[canonical(tl.Path, cache)]
		if pkg == "" && tl.Target != "" {
			pkg = owner[canonical(tl.Target, cache)]
		}
		if pkg == "" {
			continue
		}
		name, _, _ := strings.Cut(pkg, ":")
		tools[i].Manager, tools[i].Package, tools[i].PackageID, tools[i].Version = "apt", name, pkg, version[pkg]
	}
	return nil
}

// otherOwners claims tools no dpkg package owns for the other managers,
// and reads the version of other Go programs from their build info.
func (t *Tools) otherOwners(ctx context.Context, tools []Tool) {
	c := t.cfg.Extra
	cache := map[string]string{}
	dirOf := func(p string) string { return path.Dir(canonical(p, cache)) }
	canonDir := func(d string) string {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return r
		}
		return d
	}
	goBin, cargoBin := canonDir(GoBin(c)), canonDir(filepath.Join(CargoHome(c), "bin"))
	// Flatpak exports, by canonical path (/var/lib/flatpak may be a symlink).
	exports := map[string]string{canonDir(filepath.Join(c.dataHome(), "flatpak/exports/bin")): "user"}
	for _, r := range flatpakRoots(c) {
		exports[canonDir(filepath.Join(r, "exports/bin"))] = "system"
	}
	localBin := canonDir(filepath.Join(c.Home, ".local/bin"))
	var goPkgs, cargoPkgs, npmPkgs, flatPkgs, snapPkgs, pipPkgs map[string]ExtraPackage
	index := func(l ExtraList, key func(ExtraPackage) []string) map[string]ExtraPackage {
		m := map[string]ExtraPackage{}
		for _, p := range l.Packages {
			for _, k := range key(p) {
				m[k] = p
			}
		}
		return m
	}
	set := func(i int, mgr string, p ExtraPackage) {
		tools[i].Manager, tools[i].Package, tools[i].PackageID, tools[i].Version = mgr, p.Name, p.ID, p.Version
	}
	goReads := 0
	for i, tl := range tools {
		if tl.Manager != "" {
			continue
		}
		dir := dirOf(tl.Path)
		resolved := tl.Path
		if tl.Target != "" {
			resolved = tl.Target
		}
		switch {
		case dir == goBin:
			if goPkgs == nil {
				goPkgs = index(GoBinaries(c), func(p ExtraPackage) []string { return []string{p.ID} })
			}
			if p, ok := goPkgs[tl.Name]; ok {
				set(i, "go", p)
				continue
			}
		case dir == cargoBin:
			if cargoPkgs == nil {
				cargoPkgs = index(Cargo(c), func(p ExtraPackage) []string { return p.Bins })
			}
			if p, ok := cargoPkgs[tl.Name]; ok {
				set(i, "cargo", p)
				continue
			}
		case dir == localBin:
			if pipPkgs == nil {
				pipPkgs = index(Pip(c), func(p ExtraPackage) []string { return p.Bins })
			}
			if p, ok := pipPkgs[tl.Name]; ok {
				set(i, "pip", p)
				continue
			}
		case exports[dir] != "":
			if flatPkgs == nil {
				flatPkgs = index(Flatpak(c), func(p ExtraPackage) []string {
					inst, _, _ := strings.Cut(p.ID, "/")
					return []string{inst + "/" + p.Name}
				})
			}
			if p, ok := flatPkgs[exports[dir]+"/"+tl.Name]; ok {
				set(i, "flatpak", p)
				continue
			}
		case dir == "/snap/bin":
			if snapPkgs == nil {
				snapPkgs = index(Snap(ctx, c), func(p ExtraPackage) []string { return []string{p.Name} })
			}
			name, _, _ := strings.Cut(tl.Name, ".")
			if p, ok := snapPkgs[name]; ok {
				set(i, "snap", p)
				continue
			}
		}
		if mod := npmModule(resolved, NpmPrefixes(c)); mod != "" {
			if npmPkgs == nil {
				npmPkgs = index(Npm(c), func(p ExtraPackage) []string { return []string{p.ID} })
			}
			if p, ok := npmPkgs[mod]; ok {
				set(i, "npm", p)
				continue
			}
		}
		// Any other Go program says its version in its build info.
		if goReads < 200 {
			goReads++
			if bi, err := buildinfo.ReadFile(resolved); err == nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				tools[i].Version = clipText(bi.Main.Version, 100)
			}
		}
	}
}

// npmModule is "<prefix>::<package>" when p is inside a global npm
// package (<prefix>/lib/node_modules/<package>/...), else "".
func npmModule(p string, prefixes []string) string {
	for _, prefix := range prefixes {
		rest, ok := strings.CutPrefix(p, filepath.Join(prefix, "lib/node_modules")+"/")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, "/", 3)
		name := f[0]
		if strings.HasPrefix(name, "@") && len(f) > 1 {
			name += "/" + f[1]
		}
		return prefix + "::" + name
	}
	return ""
}

// whatisRe is one line of apropos -l: "name (1) - summary"; a page can
// have several names, "a, b (1) - ...".
var whatisRe = regexp.MustCompile(`^(.+?) \(([0-9][a-z0-9]*)\)\s+- (.*)$`)

// AproposArgs list every page of sections 1, 8 and 6 in one run.
var AproposArgs = []string{"-s", "1:8:6", "-l", "."}

// ParseWhatis reads apropos -l output: name -> summary, preferring
// section 1, then 8, then 6.
func ParseWhatis(out string) map[string]string {
	rank := map[string]int{"1": 0, "8": 1, "6": 2}
	best := map[string]int{}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		g := whatisRe.FindStringSubmatch(line)
		if g == nil {
			continue
		}
		r, ok := rank[g[2][:1]]
		if !ok {
			r = 3
		}
		for _, name := range strings.Split(g[1], ", ") {
			name = strings.TrimSpace(name)
			if b, seen := best[name]; seen && b <= r {
				continue
			}
			best[name], m[name] = r, clipText(strings.TrimSpace(g[3]), 300)
		}
	}
	return m
}

func (t *Tools) summaries(ctx context.Context, tools []Tool) {
	if t.cfg.Run == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := t.cfg.Run(ctx, "apropos-all", AproposArgs...)
	if len(res.Stdout) == 0 { // apropos exits 16 when there are no pages at all
		t.cfg.Log.Debug("tools: no man page summaries", "err", err)
		return
	}
	m := ParseWhatis(string(res.Stdout))
	for i, tl := range tools {
		// A link without a page of its own (editor) has its target's.
		if s := m[tl.Name]; s != "" {
			tools[i].Summary = s
		} else if tl.Target != "" {
			tools[i].Summary = m[filepath.Base(tl.Target)]
		}
	}
}

// HandleList serves GET /api/v1/tools.
func (t *Tools) HandleList(w http.ResponseWriter, r *http.Request) {
	tools, err := t.List(r.Context())
	if err != nil {
		http.Error(w, "the tools could not be listed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"dirs": t.Dirs(), "tools": tools})
}

// ManArgs are man's arguments for one page as plain text (man removes
// the formatting when its output is not a terminal).
func ManArgs(name string) []string { return []string{"-P", "cat", "--", name} }

// HandleMan serves GET /api/v1/tools/man?name=<tool>: the tool's man page
// as text. Only listed tools are asked about.
func (t *Tools) HandleMan(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !ToolNameRe.MatchString(name) {
		http.Error(w, "name must be a program name", http.StatusBadRequest)
		return
	}
	if _, ok, err := t.Find(r.Context(), name); err != nil || !ok {
		http.Error(w, "no such tool", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res, err := t.cfg.Run(ctx, "man-page", ManArgs(name)...)
	if len(res.Stdout) == 0 {
		if err != nil {
			t.cfg.Log.Debug("tools: man", "name", name, "err", err)
		}
		http.Error(w, "no manual page", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"name": name, "text": strings.ToValidUTF8(string(res.Stdout), "�"), "truncated": res.Truncated})
}
