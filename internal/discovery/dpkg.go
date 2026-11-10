package discovery

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debian packages (SPEC 4, ROADMAP 3.1) from dpkg's own database:
// /var/lib/dpkg/status and apt's /var/lib/apt/extended_states. Both are
// world-readable; nothing here needs root or runs dpkg.

// PkgsTopic carries {"changed": true} after dpkg's database changes. It is
// watched only while it has subscribers; the list itself is fetched from
// GET /api/v1/pkgs (thousands of packages are too many to push).
const PkgsTopic = "pkgs"

// Package is one package dpkg knows, installed or with config files left.
type Package struct {
	Name         string `json:"name"`
	Arch         string `json:"arch"`
	Version      string `json:"version"`
	State        string `json:"state"` // installed, config-files, half-installed, unpacked, ...
	Hold         bool   `json:"hold,omitempty"`
	Auto         bool   `json:"auto,omitempty"` // installed as a dependency (apt-mark auto)
	Essential    bool   `json:"essential,omitempty"`
	Protected    bool   `json:"protected,omitempty"`
	Priority     string `json:"priority,omitempty"`
	Section      string `json:"section,omitempty"`
	InstalledKiB int64  `json:"installed_kib,omitempty"`
	Source       string `json:"source,omitempty"`
	Maintainer   string `json:"maintainer,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Depends      string `json:"depends,omitempty"`
	Provides     string `json:"provides,omitempty"`
}

// ID is "name:arch", as dpkg and apt name a package.
func (p Package) ID() string { return p.Name + ":" + p.Arch }

// Undeletable reports why removing p must be refused (SECURITY S16), or "".
func (p Package) Undeletable() string {
	switch {
	case p.Name == "hearth":
		return "hearth"
	case p.Essential:
		return "essential"
	case p.Protected:
		return "protected"
	case p.Priority == "required":
		return "required"
	}
	return ""
}

// maxField bounds a kept field; Depends lines can be long, but not this.
const maxField = 4096

// ParseStatus reads dpkg's status file. Packages that are not installed
// (state "not-installed", left from a purge) are skipped.
func ParseStatus(r io.Reader) ([]Package, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var out []Package
	var p Package
	var key string
	flush := func() {
		if p.Name != "" && p.State != "" && p.State != "not-installed" {
			out = append(out, p)
		}
		p, key = Package{}, ""
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if key == "Depends" && len(p.Depends) < maxField {
				p.Depends += " " + strings.TrimSpace(line)
			}
			continue // other continuations (descriptions, conffiles) are not kept
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, v = k, strings.TrimSpace(v)
		switch k {
		case "Package":
			p.Name = v
		case "Architecture":
			p.Arch = v
		case "Version":
			p.Version = v
		case "Status": // want flag state, e.g. "hold ok installed"
			if f := strings.Fields(v); len(f) == 3 {
				p.Hold, p.State = f[0] == "hold", f[2]
			}
		case "Essential":
			p.Essential = v == "yes"
		case "Protected":
			p.Protected = v == "yes"
		case "Priority":
			p.Priority = v
		case "Section":
			p.Section = v
		case "Installed-Size":
			p.InstalledKiB, _ = strconv.ParseInt(v, 10, 64)
		case "Source":
			p.Source, _, _ = strings.Cut(v, " ") // "src (version)"
		case "Maintainer":
			p.Maintainer = clip(v)
		case "Description":
			p.Summary = clip(v)
		case "Provides":
			p.Provides = clip(v)
		case "Depends", "Pre-Depends":
			if p.Depends != "" {
				p.Depends += ", "
			}
			p.Depends = clip(p.Depends + v)
			key = "Depends"
		}
	}
	flush()
	return out, sc.Err()
}

func clip(s string) string {
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// ParseExtendedStates reads apt's extended_states: the "name:arch" of
// every package marked Auto-Installed.
func ParseExtendedStates(r io.Reader) (map[string]bool, error) {
	auto := map[string]bool{}
	sc := bufio.NewScanner(r)
	var name, arch string
	var on bool
	flush := func() {
		if name != "" && on {
			auto[name+":"+arch] = true
		}
		name, arch, on = "", "", false
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		switch v = strings.TrimSpace(v); k {
		case "Package":
			name = v
		case "Architecture":
			arch = v
		case "Auto-Installed":
			on = v == "1"
		}
	}
	flush()
	return auto, sc.Err()
}

// DpkgConfig configures the dpkg database reader.
type DpkgConfig struct {
	Status        string // default /var/lib/dpkg/status
	ExtendedState string // default /var/lib/apt/extended_states
	Pub           TopicPublisher
	Log           *slog.Logger
}

// Dpkg reads the installed packages, caching the list until the files
// change.
type Dpkg struct {
	cfg DpkgConfig

	mu     sync.Mutex
	cached []Package
	stamp  string // the files' sizes and times when cached
}

// NewDpkg returns a Dpkg.
func NewDpkg(cfg DpkgConfig) *Dpkg {
	if cfg.Status == "" {
		cfg.Status = "/var/lib/dpkg/status"
	}
	if cfg.ExtendedState == "" {
		cfg.ExtendedState = "/var/lib/apt/extended_states"
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Dpkg{cfg: cfg}
}

func stampOf(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			b.WriteString(strconv.FormatInt(fi.Size(), 10) + "/" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ";")
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// Packages returns the packages, sorted by name then architecture. The
// result is shared: callers must not change it.
func (d *Dpkg) Packages() ([]Package, error) {
	stamp := stampOf(d.cfg.Status, d.cfg.ExtendedState)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil && stamp == d.stamp {
		return d.cached, nil
	}
	b, err := os.ReadFile(d.cfg.Status)
	if err != nil {
		return nil, err
	}
	pkgs, err := ParseStatus(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	auto := map[string]bool{}
	if f, err := os.Open(d.cfg.ExtendedState); err == nil {
		auto, err = ParseExtendedStates(f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	for i := range pkgs {
		// apt records an "all" package under the native architecture.
		pkgs[i].Auto = auto[pkgs[i].ID()] || (pkgs[i].Arch == "all" && auto[pkgs[i].Name+":"+nativeArch()])
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Name != pkgs[j].Name {
			return pkgs[i].Name < pkgs[j].Name
		}
		return pkgs[i].Arch < pkgs[j].Arch
	})
	d.cached, d.stamp = pkgs, stamp
	return pkgs, nil
}

// ByName returns the packages named name (one per architecture).
func (d *Dpkg) ByName(name string) ([]Package, error) {
	pkgs, err := d.Packages()
	if err != nil {
		return nil, err
	}
	i := sort.Search(len(pkgs), func(i int) bool { return pkgs[i].Name >= name })
	var out []Package
	for ; i < len(pkgs) && pkgs[i].Name == name; i++ {
		out = append(out, pkgs[i])
	}
	return out, nil
}

// Run watches dpkg's database while PkgsTopic has subscribers and tells
// them when it changed (debounced 500 ms: an apt run writes it often).
// dpkg replaces the status file by renaming, so the directories are
// watched.
func (d *Dpkg) Run(ctx context.Context) {
	watch := d.cfg.Pub.Watch(PkgsTopic)
	var w *fsnotify.Watcher
	var events <-chan fsnotify.Event
	stop := func() {
		if w != nil {
			_ = w.Close()
			w, events = nil, nil
		}
	}
	defer stop()
	names := map[string]bool{d.cfg.Status: true, d.cfg.ExtendedState: true}
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			switch n := d.cfg.Pub.Subscribers(PkgsTopic); {
			case n > 0 && w == nil:
				var err error
				if w, err = fsnotify.NewWatcher(); err != nil {
					d.cfg.Log.Warn("pkgs: cannot watch dpkg", "err", err)
					w = nil
					continue
				}
				for p := range names {
					_ = w.Add(filepath.Dir(p))
				}
				events = w.Events
			case n == 0:
				stop()
			}
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if names[e.Name] {
				debounce = time.After(500 * time.Millisecond)
			}
		case <-debounce:
			debounce = nil
			_ = d.cfg.Pub.Publish(PkgsTopic, map[string]bool{"changed": true})
		}
	}
}

// HandleList serves GET /api/v1/pkgs.
func (d *Dpkg) HandleList(w http.ResponseWriter, _ *http.Request) {
	pkgs, err := d.Packages()
	if err != nil {
		http.Error(w, "the package database could not be read", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"manager": "dpkg", "packages": pkgs})
}

// FileStamp is the sizes and modification times of paths, for caches
// that must notice when they change.
func FileStamp(paths ...string) string { return stampOf(paths...) }

// Versions returns the version of each installed package by "name:arch",
// read as a stream and not cached: Packages keeps every description in
// memory, too much for something that runs with the UI closed.
func (d *Dpkg) Versions() (map[string]string, error) {
	f, err := os.Open(d.cfg.Status)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseVersions(f)
}

func parseVersions(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	var name, arch, version string
	installed := false
	flush := func() {
		if installed && name != "" {
			out[name+":"+arch] = version
		}
		name, arch, version, installed = "", "", "", false
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			flush()
		case bytes.HasPrefix(line, []byte("Package: ")):
			name = string(line[len("Package: "):])
		case bytes.HasPrefix(line, []byte("Architecture: ")):
			arch = string(line[len("Architecture: "):])
		case bytes.HasPrefix(line, []byte("Version: ")):
			version = string(line[len("Version: "):])
		case bytes.HasPrefix(line, []byte("Status: ")):
			installed = bytes.HasSuffix(line, []byte(" installed"))
		}
	}
	flush()
	return out, sc.Err()
}

// nativeArch is dpkg's name for the architecture Hearth runs on.
func nativeArch() string {
	switch runtime.GOARCH {
	case "386":
		return "i386"
	case "arm":
		return "armhf"
	case "ppc64le":
		return "ppc64el"
	}
	return runtime.GOARCH
}
