package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// AppsTopic is the WebSocket topic that signals a changed app list.
const AppsTopic = "apps"

// Sources of an app.
const (
	SourceSystem   = "system"
	SourceUser     = "user"
	SourceFlatpak  = "flatpak"
	SourceSnap     = "snap"
	SourceAppImage = "appimage"
)

// App is one launchable application.
type App struct {
	ID          string   `json:"id"` // desktop file ID without ".desktop", e.g. org.gnome.TextEditor
	Name        string   `json:"name"`
	GenericName string   `json:"generic_name,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	Categories  []string `json:"categories"`
	Keywords    []string `json:"keywords,omitempty"`
	Source      string   `json:"source"`
	Path        string   `json:"path"` // the .desktop file or the AppImage
	Terminal    bool     `json:"terminal,omitempty"`
	HasIcon     bool     `json:"has_icon"`

	// For launching (ROADMAP 1.11) and matching running processes.
	Exec      string `json:"-"`
	WorkDir   string `json:"-"`
	WMClass   string `json:"-"`
	FlatpakID string `json:"-"`
	IconName  string `json:"-"` // Icon=, for the %i field code
	iconPath  string
}

// idRe matches the app IDs the index produces: desktop file IDs and
// "appimage.<name>".
var idRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+~@-]{0,199}$`)

// ValidID reports whether s can be an app ID; requests are checked with
// it before any lookup.
func ValidID(s string) bool { return idRe.MatchString(s) }

// Publisher is the subset of api.Hub the index needs.
type Publisher interface {
	Publish(topic string, data any) error
}

// Config says where to look. Zero values take the XDG defaults.
type Config struct {
	Home     string
	DataHome string   // $XDG_DATA_HOME, default ~/.local/share
	DataDirs []string // $XDG_DATA_DIRS, default /usr/local/share:/usr/share
	// Desktops is $XDG_CURRENT_DESKTOP split on ":"; empty disables
	// OnlyShowIn/NotShowIn filtering.
	Desktops     []string
	Path         []string // for TryExec
	AppImageDirs []string // default ~/Applications, ~/Downloads, ~/.local/bin
	Pub          Publisher
	Log          *slog.Logger
}

// ConfigFromEnv builds a Config from the environment.
func ConfigFromEnv(getenv func(string) string) Config {
	home := getenv("HOME")
	split := func(v string) []string {
		var out []string
		for _, p := range strings.Split(v, ":") {
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	c := Config{
		Home: home, DataHome: getenv("XDG_DATA_HOME"), DataDirs: split(getenv("XDG_DATA_DIRS")),
		Desktops: split(getenv("XDG_CURRENT_DESKTOP")), Path: split(getenv("PATH")),
	}
	c.defaults()
	// Flatpak and Snap export directories, in case the session did not add
	// them to XDG_DATA_DIRS (hearthd runs under the user manager).
	for _, d := range []string{filepath.Join(c.DataHome, "flatpak/exports/share"), "/var/lib/flatpak/exports/share", "/var/lib/snapd/desktop"} {
		if !contains(c.DataDirs, d) {
			c.DataDirs = append(c.DataDirs, d)
		}
	}
	return c
}

func (c *Config) defaults() {
	if c.DataHome == "" {
		c.DataHome = filepath.Join(c.Home, ".local/share")
	}
	if len(c.DataDirs) == 0 {
		c.DataDirs = []string{"/usr/local/share", "/usr/share"}
	}
	if len(c.Path) == 0 {
		c.Path = []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/games"}
	}
	if c.AppImageDirs == nil {
		c.AppImageDirs = []string{filepath.Join(c.Home, "Applications"), filepath.Join(c.Home, "Downloads"), filepath.Join(c.Home, ".local/bin")}
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if filepath.Clean(v) == filepath.Clean(s) {
			return true
		}
	}
	return false
}

// Index is the current app list.
type Index struct {
	cfg Config

	mu      sync.RWMutex
	apps    []App
	byID    map[string]int
	byAlias map[string]int // lowercased ID, Flatpak ID, StartupWMClass
	digest  [32]byte
}

// NewIndex returns an Index; call Scan (or Run) to fill it.
func NewIndex(cfg Config) *Index {
	cfg.defaults()
	return &Index{cfg: cfg, byID: map[string]int{}, byAlias: map[string]int{}}
}

// appDirs are the applications directories, highest precedence first.
func (x *Index) appDirs() []string {
	dirs := []string{filepath.Join(x.cfg.DataHome, "applications")}
	for _, d := range x.cfg.DataDirs {
		dirs = append(dirs, filepath.Join(d, "applications"))
	}
	return dirs
}

func (x *Index) source(dir string) string {
	switch {
	case strings.Contains(dir, "/flatpak/exports/"):
		return SourceFlatpak
	case strings.HasPrefix(dir, "/var/lib/snapd/desktop"):
		return SourceSnap
	case strings.HasPrefix(dir, filepath.Join(x.cfg.DataHome, "applications")):
		return SourceUser
	}
	return SourceSystem
}

// Scan rebuilds the list and reports whether it changed.
func (x *Index) Scan() bool {
	icons := x.iconIndex()
	seen := map[string]bool{} // desktop IDs already decided (shown or hidden)
	var apps []App
	for _, dir := range x.appDirs() {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".desktop") {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return nil //nolint:nilerr // cannot happen for paths under dir
			}
			id := strings.TrimSuffix(strings.ReplaceAll(rel, "/", "-"), ".desktop")
			if seen[id] {
				return nil // a higher-precedence file with this ID won
			}
			b, err := readLimited(p, maxDesktopFile)
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			e, ok := parseDesktop(b)
			if !ok {
				return nil
			}
			seen[id] = true // Hidden=true in a user file deletes the system one too
			if !x.show(e) {
				return nil
			}
			src := x.source(dir)
			if e.FlatpakID != "" {
				src = SourceFlatpak
			}
			a := App{ID: id, Name: e.Name, GenericName: e.GenericName, Comment: e.Comment,
				Categories: nonNil(e.Categories), Keywords: e.Keywords, Source: src, Path: p, Terminal: e.Terminal,
				Exec: e.Exec, WorkDir: e.Path, WMClass: e.WMClass, FlatpakID: e.FlatpakID, IconName: e.Icon}
			a.iconPath = x.icon(e.Icon, icons)
			a.HasIcon = a.iconPath != ""
			apps = append(apps, a)
			return nil
		})
	}
	apps = append(apps, x.appImages()...)
	sort.Slice(apps, func(i, j int) bool {
		a, b := strings.ToLower(apps[i].Name), strings.ToLower(apps[j].Name)
		if a != b {
			return a < b
		}
		return apps[i].ID < apps[j].ID
	})

	raw, _ := json.Marshal(apps)
	digest := sha256.Sum256(raw)
	byID := make(map[string]int, len(apps))
	byAlias := make(map[string]int, len(apps)*2)
	for i, a := range apps {
		byID[a.ID] = i
		for _, k := range []string{a.ID, a.FlatpakID, a.WMClass} {
			if k != "" {
				if _, dup := byAlias[strings.ToLower(k)]; !dup {
					byAlias[strings.ToLower(k)] = i
				}
			}
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	changed := digest != x.digest
	x.apps, x.byID, x.byAlias, x.digest = apps, byID, byAlias, digest
	return changed
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// show applies the spec's visibility rules.
func (x *Index) show(e entry) bool {
	if e.Type != "Application" || e.NoDisplay || e.Hidden || e.Name == "" || e.Exec == "" {
		return false
	}
	if len(x.cfg.Desktops) > 0 {
		if len(e.OnlyShowIn) > 0 && !overlaps(e.OnlyShowIn, x.cfg.Desktops) {
			return false
		}
		if overlaps(e.NotShowIn, x.cfg.Desktops) {
			return false
		}
	}
	if e.TryExec != "" && !x.executable(e.TryExec) {
		return false
	}
	return true
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

func (x *Index) executable(name string) bool {
	isExec := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
	}
	if filepath.IsAbs(name) {
		return isExec(name)
	}
	if strings.Contains(name, "/") {
		return false
	}
	for _, d := range append(append([]string{}, x.cfg.Path...), filepath.Join(x.cfg.Home, ".local/bin")) {
		if isExec(filepath.Join(d, name)) {
			return true
		}
	}
	return false
}

// AppImages: "Obsidian-1.5.3.AppImage" -> "Obsidian".
var (
	appImageExt     = regexp.MustCompile(`(?i)\.appimage$`)
	appImageVersion = regexp.MustCompile(`(?i)([-_ ]v?\d.*|[-_.](x86_64|amd64|aarch64|arm64|x64))$`)
	idUnsafe        = regexp.MustCompile(`[^a-z0-9.]+`)
)

func (x *Index) appImages() []App {
	var out []App
	for _, dir := range x.cfg.AppImageDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !appImageExt.MatchString(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			base := appImageExt.ReplaceAllString(e.Name(), "")
			name := appImageVersion.ReplaceAllString(base, "")
			if name == "" {
				name = base
			}
			id := "appimage." + strings.Trim(idUnsafe.ReplaceAllString(strings.ToLower(base), "-"), "-.")
			out = append(out, App{ID: id, Name: name, Categories: []string{}, Source: SourceAppImage, Path: p, Exec: p})
		}
	}
	return out
}

// Apps returns the current list (shared; do not modify).
func (x *Index) Apps() []App {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.apps
}

// App returns the app with id.
func (x *Index) App(id string) (App, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	i, ok := x.byID[id]
	if !ok {
		return App{}, false
	}
	return x.apps[i], true
}

// Name returns the display name for an app ID as systemd scopes carry it
// (desktop file ID, Flatpak ID or WM class, any case).
func (x *Index) Name(appID string) (string, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	if i, ok := x.byAlias[strings.ToLower(appID)]; ok {
		return x.apps[i].Name, true
	}
	return "", false
}

// Running maps app IDs taken from unit names (collect.RunningApps) to the
// desktop IDs they belong to; one app can run under several units (one
// started by Hearth, one by the desktop). IDs of no known app are dropped.
func (x *Index) Running(unitIDs []string) map[string][]string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := map[string][]string{}
	for _, u := range unitIDs {
		if i, ok := x.byAlias[strings.ToLower(u)]; ok {
			id := x.apps[i].ID
			out[id] = append(out[id], u)
		}
	}
	return out
}

// Run scans, then rescans after changes to the watched directories
// (debounced 500 ms, ARCHITECTURE "Discovery") until ctx ends.
func (x *Index) Run(ctx context.Context) {
	x.Scan()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		x.cfg.Log.Warn("apps: cannot watch for changes", "err", err)
		return
	}
	defer func() { _ = w.Close() }()
	for _, d := range append(x.appDirs(), x.cfg.AppImageDirs...) {
		_ = w.Add(d) // missing directories are not watched
	}
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.Events:
			if !ok {
				return
			}
			debounce = time.After(500 * time.Millisecond)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			x.cfg.Log.Warn("apps: watch error", "err", err)
		case <-debounce:
			debounce = nil
			if x.Scan() && x.cfg.Pub != nil {
				_ = x.cfg.Pub.Publish(AppsTopic, map[string]int{"count": len(x.Apps())})
			}
		}
	}
}

// readLimited reads at most limit bytes of p; a larger file is an error.
func readLimited(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p) // #nosec G304 -- callers pass paths the index found or resolved itself
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fs.ErrInvalid
	}
	return b, nil
}

// Icons ---------------------------------------------------------------

// iconSizes in order of preference: big enough for HiDPI tiles, small
// enough to send as data: URIs.
var iconSizes = []string{"128x128", "scalable", "96x96", "64x64", "256x256", "48x48", "512x512", "32x32"}

// iconThemes searched, in order. hicolor is the spec's fallback and holds
// nearly all application icons; "gnome" has the legacy category icons
// that older entries (settings panels, input tools) still name.
var iconThemes = []string{"Adwaita", "hicolor", "gnome"}

// iconContexts: application icons first, then the generic ones some
// entries use (preferences-system, input-keyboard).
var iconContexts = []string{"apps", "categories", "devices", "places", "mimetypes", "status", "actions"}

// iconIndex maps icon names to files.
func (x *Index) iconIndex() map[string]string {
	bases := []string{filepath.Join(x.cfg.DataHome, "icons"), filepath.Join(x.cfg.Home, ".icons")}
	for _, d := range x.cfg.DataDirs {
		bases = append(bases, filepath.Join(d, "icons"))
	}
	idx := map[string]string{}
	add := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name, ext := splitExt(e.Name())
			if (ext == ".png" || ext == ".svg") && idx[name] == "" {
				idx[name] = filepath.Join(dir, e.Name())
			}
		}
	}
	for _, ctx := range iconContexts {
		for _, theme := range iconThemes {
			for _, size := range iconSizes {
				for _, b := range bases {
					add(filepath.Join(b, theme, size, ctx))
				}
			}
		}
	}
	for _, d := range x.cfg.DataDirs {
		add(filepath.Join(d, "pixmaps"))
	}
	return idx
}

func splitExt(name string) (string, string) {
	ext := strings.ToLower(filepath.Ext(name))
	return strings.TrimSuffix(name, filepath.Ext(name)), ext
}

// icon resolves an Icon= value to a PNG or SVG file.
func (x *Index) icon(v string, idx map[string]string) string {
	if v == "" {
		return ""
	}
	if filepath.IsAbs(v) {
		_, ext := splitExt(v)
		if fi, err := os.Stat(v); err == nil && fi.Mode().IsRegular() && (ext == ".png" || ext == ".svg") {
			return v
		}
		return ""
	}
	if p := idx[v]; p != "" {
		return p
	}
	// Some entries name the file with its extension.
	if name, ext := splitExt(v); ext == ".png" || ext == ".svg" {
		return idx[name]
	}
	return ""
}

// maxIcon bounds an icon sent to the UI.
const maxIcon = 512 << 10

// IconData returns the app's icon as a data: URI, or "" if it has none.
// Icons are only ever read from paths the index resolved itself.
func (x *Index) IconData(id string) string {
	a, ok := x.App(id)
	if !ok || a.iconPath == "" {
		return ""
	}
	b, err := readLimited(a.iconPath, maxIcon)
	if err != nil || len(b) == 0 {
		return ""
	}
	mime := "image/png"
	if strings.HasSuffix(strings.ToLower(a.iconPath), ".svg") {
		mime = "image/svg+xml"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}
