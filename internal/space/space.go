// Package space is the Space advisor (SPEC 18, ROADMAP 4.7, SECURITY
// S16c): what takes room and could go, measured without privileges, and
// the actions that free it. Docker leftovers and old kernels are shown
// here and removed by their own actions (prune/*, package/remove).
package space

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Limits.
const (
	maxSelected = 16 // items trashed by one confirmation
	maxListed   = 30 // cache folders and trash entries shown
	maxLarge    = 50 // large files shown
	// minLarge is the smallest file worth listing.
	minLarge    = 100 << 20
	scanTimeout = 30 * time.Second
	largeTime   = time.Minute
	keptKernels = 2 // newest kernels never offered, besides the running one
)

// Reasons an item is blocked or failed; the UI translates them.
const (
	ReasonChanged      = "changed" // not what the preview showed
	ReasonRefused      = "refused" // not something the advisor offers
	ReasonUnauthorized = "unauthorized"
	ReasonError        = "error"
)

// Broker is the subset of broker.Client the cleanups need.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Runner runs a command as the user (pkgs.UserRunner.Run).
type Runner func(ctx context.Context, desc, path string, argv, env []string) (string, error)

// Config configures an Advisor.
type Config struct {
	Home     string
	CacheDir string // default ~/.cache
	TrashDir string // default ~/.local/share/Trash
	Archives string // default broker.AptArchives
	Journal  string // default /var/log/journal
	Gio      string // default /usr/bin/gio
	Broker   Broker
	Run      Runner
	// Packages lists the dpkg packages (discovery.Dpkg).
	Packages func() ([]discovery.Package, error)
	// Release is the running kernel's release.
	Release func() string
}

// Advisor measures and cleans.
type Advisor struct {
	cfg   Config
	large sync.Mutex // one scan of the home at a time
}

// New returns an Advisor; folders are resolved so paths compare exactly.
func New(cfg Config) *Advisor {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(cfg.Home, ".cache")
	}
	if cfg.TrashDir == "" {
		cfg.TrashDir = filepath.Join(cfg.Home, ".local/share/Trash")
	}
	if cfg.Archives == "" {
		cfg.Archives = broker.AptArchives
	}
	if cfg.Journal == "" {
		cfg.Journal = "/var/log/journal"
	}
	if cfg.Gio == "" {
		cfg.Gio = "/usr/bin/gio"
	}
	if cfg.Release == nil {
		cfg.Release = func() string { return "" }
	}
	cfg.Home, cfg.CacheDir, cfg.TrashDir = real(cfg.Home), real(cfg.CacheDir), real(cfg.TrashDir)
	return &Advisor{cfg: cfg}
}

// Entry is a folder or file and its size.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Time int64  `json:"time,omitempty"` // unix seconds, when modified
}

// Group is a measured place.
type Group struct {
	Size  int64   `json:"size"`
	Count int     `json:"count"`
	Items []Entry `json:"items,omitempty"`
}

// Kernel is an old kernel's package.
type Kernel struct {
	Package string `json:"package"` // name:arch, as package/remove takes it
	Kernel  string `json:"kernel"`
	Size    int64  `json:"size"`
}

// Report is what GET /api/v1/space shows.
type Report struct {
	Apt     Group    `json:"apt"`
	Journal Group    `json:"journal"`
	Cache   Group    `json:"cache"`
	Trash   Group    `json:"trash"`
	Kernels []Kernel `json:"kernels"`
	Running string   `json:"running"`
	// CanTrash is false without gio (package libglib2.0-bin).
	CanTrash bool  `json:"can_trash"`
	Keep     []int `json:"journal_keep"`
}

// allocated is what a file takes on disk.
func allocated(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// measure sums what is under path, staying on its filesystem.
func measure(ctx context.Context, path string) (size int64, files int) {
	root, err := os.Lstat(path)
	if err != nil {
		return 0, 0
	}
	size = allocated(root)
	if !root.IsDir() {
		return size, 1
	}
	st, _ := root.Sys().(*syscall.Stat_t)
	stack := []string{path}
	for len(stack) > 0 && ctx.Err() == nil {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			size += allocated(fi)
			if fi.IsDir() {
				if s, ok := fi.Sys().(*syscall.Stat_t); !ok || st == nil || s.Dev == st.Dev {
					stack = append(stack, filepath.Join(dir, e.Name()))
				}
				continue
			}
			files++
		}
	}
	return size, files
}

// children measures dir's direct children, largest first.
func children(ctx context.Context, dir string) Group {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Group{}
	}
	var g Group
	for _, e := range entries {
		if !e.IsDir() && !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		size, _ := measure(ctx, p)
		g.Size += size
		g.Count++
		g.Items = append(g.Items, Entry{Name: e.Name(), Path: p, Size: size})
	}
	sort.SliceStable(g.Items, func(i, j int) bool { return g.Items[i].Size > g.Items[j].Size })
	g.Items = g.Items[:min(len(g.Items), maxListed)]
	return g
}

// debs measures dir's .deb files.
func debs(dir string) Group {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Group{}
	}
	var g Group
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".deb") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			g.Size += allocated(fi)
			g.Count++
		}
	}
	return g
}

// Report measures everything; what cannot be read counts as nothing.
func (a *Advisor) Report(ctx context.Context) Report {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	out := Report{Running: a.cfg.Release(), Kernels: []Kernel{}, Keep: broker.JournalKeep}
	out.Apt = debs(a.cfg.Archives)
	size, files := measure(ctx, a.cfg.Journal)
	out.Journal = Group{Size: size, Count: files}
	out.Cache = children(ctx, a.cfg.CacheDir)
	out.Trash = children(ctx, filepath.Join(a.cfg.TrashDir, "files"))
	_, err := os.Stat(a.cfg.Gio)
	out.CanTrash = err == nil
	if a.cfg.Packages != nil {
		if pkgs, err := a.cfg.Packages(); err == nil {
			out.Kernels = oldKernels(pkgs, out.Running)
		}
	}
	return out
}

var (
	imageRe = regexp.MustCompile(`^linux-image-([0-9]+\.[0-9]+.*)$`)
	numRe   = regexp.MustCompile(`[0-9]+|[^0-9]+`)
	kinds   = []string{"linux-image-", "linux-headers-", "linux-modules-", "linux-kbuild-"}
)

// CompareKernels orders releases, numbers as numbers (6.12.9 < 6.12.10).
func CompareKernels(a, b string) int {
	x, y := numRe.FindAllString(a, -1), numRe.FindAllString(b, -1)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		n, err1 := strconv.Atoi(x[i])
		m, err2 := strconv.Atoi(y[i])
		if err1 == nil && err2 == nil {
			if n < m {
				return -1
			}
			return 1
		}
		return strings.Compare(x[i], y[i])
	}
	return len(x) - len(y)
}

// oldKernels lists the packages of kernels neither running nor among the
// newest. Without knowing what runs it offers nothing.
func oldKernels(pkgs []discovery.Package, running string) []Kernel {
	out := []Kernel{}
	if running == "" {
		return out
	}
	var releases []string
	for _, p := range pkgs {
		if m := imageRe.FindStringSubmatch(p.Name); m != nil && p.State == "installed" && !strings.HasSuffix(m[1], "-dbg") && !strings.HasSuffix(m[1], "-unsigned") {
			releases = append(releases, m[1])
		}
	}
	sort.Slice(releases, func(i, j int) bool { return CompareKernels(releases[i], releases[j]) > 0 })
	if len(releases) <= keptKernels {
		return out
	}
	for _, rel := range releases[keptKernels:] {
		if rel == running {
			continue
		}
		abi := rel // "6.12.1+deb13" of "6.12.1+deb13-amd64": headers-common, kbuild
		if i := strings.LastIndexByte(rel, '-'); i > 0 {
			abi = rel[:i]
		}
		for _, p := range pkgs {
			if p.State != "installed" {
				continue
			}
			for _, k := range kinds {
				rest, ok := strings.CutPrefix(p.Name, k)
				if ok && (rest == rel || rest == abi || strings.HasPrefix(rest, abi+"-")) && !sameABI(rest, running) {
					out = append(out, Kernel{Package: p.ID(), Kernel: rel, Size: p.InstalledKiB << 10})
				}
			}
		}
	}
	return out
}

// sameABI reports whether a package's name part belongs to the running
// kernel (another flavour of the same version shares its headers).
func sameABI(rest, running string) bool {
	if rest == running {
		return true
	}
	if i := strings.LastIndexByte(running, '-'); i > 0 {
		return rest == running[:i] || strings.HasPrefix(rest, running[:i]+"-")
	}
	return false
}

// LargeResult is what GET /api/v1/space/large shows.
type LargeResult struct {
	Items []Entry `json:"items"`
	// Truncated says the scan ran out of time.
	Truncated bool `json:"truncated"`
}

// Large walks the home for the largest files, leaving out hidden folders
// (settings, caches, game libraries: not things to pick from a list).
func (a *Advisor) Large(ctx context.Context) (LargeResult, error) {
	if !a.large.TryLock() {
		return LargeResult{}, errors.New("already looking")
	}
	defer a.large.Unlock()
	ctx, cancel := context.WithTimeout(ctx, largeTime)
	defer cancel()
	root, err := os.Lstat(a.cfg.Home)
	if err != nil {
		return LargeResult{}, err
	}
	st, ok := root.Sys().(*syscall.Stat_t)
	if !ok {
		return LargeResult{}, errors.New("space: no file information")
	}
	out := LargeResult{Items: []Entry{}}
	uid := uint32(os.Getuid()) // #nosec G115 -- a uid
	stack := []string{a.cfg.Home}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if fi, err := e.Info(); err == nil {
					if s, ok := fi.Sys().(*syscall.Stat_t); ok && s.Dev == st.Dev {
						stack = append(stack, p)
					}
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			fi, err := e.Info()
			if err != nil || allocated(fi) < minLarge {
				continue
			}
			if s, ok := fi.Sys().(*syscall.Stat_t); !ok || s.Uid != uid || s.Dev != st.Dev {
				continue
			}
			out.Items = append(out.Items, Entry{Name: e.Name(), Path: p, Size: allocated(fi), Time: fi.ModTime().Unix()})
		}
		if len(out.Items) > 4*maxLarge {
			out.Items = top(out.Items)
		}
	}
	out.Items = top(out.Items)
	return out, nil
}

func top(items []Entry) []Entry {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	return items[:min(len(items), maxLarge)]
}

// picked is a path as the preview saw it.
type picked struct {
	path     string
	dev, ino uint64
}

// check says whether path may be trashed as a target ("cache" or "large").
func (a *Advisor) check(target, path string) (picked, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return picked{}, nil, errors.New("not a clean absolute path")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return picked{}, nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) { // #nosec G115 -- a uid
		return picked{}, nil, errors.New("not yours")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parent != filepath.Dir(path) {
		return picked{}, nil, errors.New("reached through a link")
	}
	switch target {
	case "cache":
		if parent != a.cfg.CacheDir || (!fi.IsDir() && !fi.Mode().IsRegular()) {
			return picked{}, nil, errors.New("not in the cache folder")
		}
	case "large":
		rel, err := filepath.Rel(a.cfg.Home, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || !fi.Mode().IsRegular() {
			return picked{}, nil, errors.New("not a file in your home")
		}
		for _, part := range strings.Split(rel, "/") {
			if strings.HasPrefix(part, ".") {
				return picked{}, nil, errors.New("in a hidden folder")
			}
		}
	default:
		return picked{}, nil, errors.New("unknown target")
	}
	return picked{path: path, dev: st.Dev, ino: st.Ino}, fi, nil
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

func (a *Advisor) planTrash(ctx context.Context, r actions.Request) (actions.Plan, error) {
	p := actions.Plan{Summary: "move to trash", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if _, err := os.Stat(a.cfg.Gio); err != nil {
		return p, actions.Errorf(http.StatusServiceUnavailable, "gio is not installed (package libglib2.0-bin)")
	}
	var state []picked
	keys := make([]string, 0, len(r.Params))
	for k := range r.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := r.Params[k]
		pk, _, err := a.check(r.Target, path)
		if err != nil {
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: path, Name: path, Detail: err.Error()}, Reason: ReasonRefused})
			continue
		}
		size, _ := measure(ctx, path)
		p.Items = append(p.Items, actions.Item{ID: path, Name: path, Detail: human(size)})
		state = append(state, pk)
	}
	p.State = state
	return p, nil
}

func (a *Advisor) runTrash(ctx context.Context, r actions.Request, plan actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	state, _ := plan.State.([]picked)
	argv := []string{"gio", "trash", "--"}
	var kept []string
	for _, want := range state {
		now, _, err := a.check(r.Target, want.path)
		if err != nil || now != want {
			res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: want.path, Name: want.path}, Reason: ReasonChanged})
			continue
		}
		kept = append(kept, want.path)
	}
	if len(kept) == 0 {
		return res, nil
	}
	if _, err := a.cfg.Run(context.WithoutCancel(ctx), "Move files to the trash", a.cfg.Gio, append(argv, kept...), nil); err != nil {
		for _, path := range kept {
			if _, serr := os.Lstat(path); serr != nil { // gio went as far as it could
				res.Done++
				continue
			}
			res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: path, Name: path, Detail: err.Error()}, Reason: ReasonError})
		}
		return res, nil
	}
	res.Done = len(kept)
	return res, nil
}

func (a *Advisor) planEmpty(ctx context.Context, _ actions.Request) (actions.Plan, error) {
	p := actions.Plan{Summary: "empty the trash", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if _, err := os.Stat(a.cfg.Gio); err != nil {
		return p, actions.Errorf(http.StatusServiceUnavailable, "gio is not installed (package libglib2.0-bin)")
	}
	g := children(ctx, filepath.Join(a.cfg.TrashDir, "files"))
	for _, e := range g.Items {
		p.Items = append(p.Items, actions.Item{ID: e.Path, Name: e.Name, Detail: human(e.Size)})
	}
	if g.Count > 0 {
		p.Confirm = "empty" // deleted for good
	}
	return p, nil
}

func (a *Advisor) runEmpty(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	if len(plan.Items) == 0 {
		return res, nil
	}
	if _, err := a.cfg.Run(context.WithoutCancel(ctx), "Empty the trash", a.cfg.Gio, []string{"gio", "trash", "--empty"}, nil); err != nil {
		res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: "trash", Name: "trash", Detail: err.Error()}, Reason: ReasonError})
		return res, nil
	}
	res.Done = len(plan.Items)
	return res, nil
}

// brokerRun calls a cleanup; a refused password is a failed item.
func (a *Advisor) brokerRun(ctx context.Context, method string, params any, it actions.Item) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	err := a.cfg.Broker.Call(ctx, method, params, nil)
	if err == nil {
		res.Done = 1
		return res, nil
	}
	var be *broker.Error
	switch {
	case !errors.As(err, &be):
		return res, actions.Errorf(http.StatusServiceUnavailable, "hearth-broker is not available")
	case be.Code == broker.CodeUnauthorized:
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonUnauthorized})
	case be.Code == broker.CodeNotMember:
		return res, actions.Errorf(http.StatusForbidden, "you are not in the hearth group")
	default:
		it.Detail = be.Message
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
	}
	return res, nil
}

// Register adds the space/* actions. All are previewed and confirmed.
func (a *Advisor) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "space", Name: "trash", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if r.Target != "cache" && r.Target != "large" {
				return errors.New(`target must be "cache" or "large"`)
			}
			if len(r.Params) == 0 || len(r.Params) > maxSelected {
				return fmt.Errorf("params must name 1 to %d paths", maxSelected)
			}
			for _, p := range r.Params {
				if !filepath.IsAbs(p) || filepath.Clean(p) != p {
					return errors.New("paths must be clean and absolute")
				}
			}
			return nil
		},
		Plan: a.planTrash, Run: a.runTrash})
	fixed := func(want string) func(actions.Request) error {
		return func(r actions.Request) error {
			if r.Target != want || len(r.Params) != 0 {
				return fmt.Errorf("target must be %q and params empty", want)
			}
			return nil
		}
	}
	reg.Register(actions.Action{Kind: "space", Name: "empty-trash", Risk: actions.Destructive,
		Validate: fixed("trash"), Plan: a.planEmpty, Run: a.runEmpty})
	reg.Register(actions.Action{Kind: "space", Name: "apt-clean", Risk: actions.Destructive, Validate: fixed("apt"),
		Plan: func(context.Context, actions.Request) (actions.Plan, error) {
			g := debs(a.cfg.Archives)
			p := actions.Plan{Summary: "clean the package cache", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
			if g.Count > 0 {
				p.Items = append(p.Items, actions.Item{ID: "apt", Name: a.cfg.Archives, Detail: human(g.Size)})
			}
			return p, nil
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			if len(p.Items) == 0 {
				return actions.Result{Failed: []actions.Blocked{}}, nil
			}
			return a.brokerRun(ctx, "apt.clean", nil, p.Items[0])
		}})
	reg.Register(actions.Action{Kind: "space", Name: "journal-vacuum", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			keep, err := strconv.Atoi(r.Target)
			if err != nil || len(r.Params) != 0 {
				return errors.New("target must be the MiB to keep and params empty")
			}
			for _, k := range broker.JournalKeep {
				if k == keep {
					return nil
				}
			}
			return errors.New("target must be 100, 200, 500 or 1000")
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			size, _ := measure(ctx, a.cfg.Journal)
			keep, _ := strconv.Atoi(r.Target)
			p := actions.Plan{Summary: "shrink the journal", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
			if size > int64(keep)<<20 {
				p.Items = append(p.Items, actions.Item{ID: "journal", Name: a.cfg.Journal, Detail: human(size) + " → " + human(int64(keep)<<20)})
			}
			return p, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			if len(p.Items) == 0 {
				return actions.Result{Failed: []actions.Blocked{}}, nil
			}
			keep, _ := strconv.Atoi(r.Target)
			return a.brokerRun(ctx, "journal.vacuum", broker.JournalParams{KeepMiB: keep}, p.Items[0])
		}})
}

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// KeptKernel reports whether the package name is a kernel package that is
// never removed: the running kernel's, one of the two newest, or a kernel
// package this rule cannot place (meta packages such as linux-image-amd64
// included). Only what the advisor would offer as an old kernel may go
// (SECURITY S16c, S16k). Without the running release, every kernel
// package is kept.
func KeptKernel(pkgs []discovery.Package, running, name string) bool {
	kernel := false
	for _, k := range kinds {
		if strings.HasPrefix(name, k) {
			kernel = true
			break
		}
	}
	if !kernel {
		return false
	}
	for _, k := range oldKernels(pkgs, running) {
		if n, _, _ := strings.Cut(k.Package, ":"); n == name {
			return false
		}
	}
	return true
}
