// Package session changes the user's desktop session settings a mode sets
// (SPEC 23, ROADMAP 10.3): the wallpaper and Do Not Disturb, GNOME first,
// with other desktops detected and refused by name. Brightness (logind),
// the power profile (power-profiles-daemon), NetworkManager connections and
// opening apps are actions of their own packages; this package picks the
// backlight a mode's brightness applies to and reads the current values, so
// a mode can put them back.
//
// Everything runs as the user. GNOME settings go through gsettings with a
// closed list of keys (safeexec), never a shell.
package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Reasons a session action is blocked; the UI translates them.
const (
	ReasonDesktop     = "desktop"     // the desktop is not supported yet; Detail names it
	ReasonUnavailable = "unavailable" // the file or device is not there
)

// GNOME settings.
const (
	schemaBackground    = "org.gnome.desktop.background"
	schemaNotifications = "org.gnome.desktop.notifications"
	keyPicture          = "picture-uri"
	keyPictureDark      = "picture-uri-dark"
	keyBanners          = "show-banners"
)

// WallpaperRoots are where a mode's wallpaper may come from: the user's
// home, or the system's backgrounds.
var WallpaperRoots = []string{"~/", "/usr/share/backgrounds/", "/usr/share/wallpapers/"}

var wallpaperExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".svg": true, ".jxl": true}

// CheckWallpaper checks a wallpaper path's shape: under WallpaperRoots,
// clean, an image file name. Where it leads is checked when it is applied.
func CheckWallpaper(p string) error {
	if len(p) > 1024 {
		return errors.New("wallpaper: the path is too long")
	}
	for _, r := range p {
		if !unicode.IsGraphic(r) {
			return errors.New("wallpaper: the path has a control character")
		}
	}
	root := ""
	for _, r := range WallpaperRoots {
		if strings.HasPrefix(p, r) {
			root = r
			break
		}
	}
	if root == "" {
		return errors.New("wallpaper: a path in your home (~/) or in /usr/share/backgrounds")
	}
	rest := strings.TrimPrefix(p, root)
	if rest == "" || path.Clean(rest) != rest || rest == ".." || strings.HasPrefix(rest, "../") || strings.HasPrefix(rest, "/") {
		return errors.New("wallpaper: the path is not clean")
	}
	if !wallpaperExt[strings.ToLower(path.Ext(rest))] {
		return errors.New("wallpaper: a JPEG, PNG, WebP, SVG or JPEG XL file")
	}
	return nil
}

// Desktop is the desktop the session runs.
type Desktop struct {
	Name  string `json:"name"`  // as XDG_CURRENT_DESKTOP names it, cleaned; "unknown" when unset
	GNOME bool   `json:"gnome"` // wallpaper and Do Not Disturb are supported
}

var desktopNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// DetectDesktop reads XDG_CURRENT_DESKTOP, a colon-separated list such as
// "ubuntu:GNOME".
func DetectDesktop(v string) Desktop {
	d := Desktop{Name: "unknown"}
	for i, part := range strings.Split(v, ":") {
		if !desktopNameRe.MatchString(part) {
			continue
		}
		if i == 0 || d.Name == "unknown" {
			d.Name = part
		}
		if strings.EqualFold(part, "GNOME") {
			d.Name, d.GNOME = "GNOME", true
		}
	}
	return d
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Config configures a Session.
type Config struct {
	Desktop Desktop
	// Home is the user's home, for "~/" wallpapers.
	Home string
	Run  Runner
	// Backlight is the sysfs class directory; default /sys/class/backlight.
	Backlight string
}

// Session reads and changes session settings.
type Session struct{ cfg Config }

// New returns a Session.
func New(cfg Config) *Session {
	if cfg.Backlight == "" {
		cfg.Backlight = "/sys/class/backlight"
	}
	if cfg.Run == nil {
		cfg.Run = safeexec.Run
	}
	return &Session{cfg: cfg}
}

// Desktop is the detected desktop.
func (s *Session) Desktop() Desktop { return s.cfg.Desktop }

func (s *Session) gsettings(ctx context.Context, verb, schema, key string, value ...string) (string, error) {
	args := append([]string{verb, schema, key}, value...)
	res, err := s.cfg.Run(ctx, "gsettings-"+verb, args...)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("session: gsettings %s %s %s: exit %d", verb, schema, key, res.ExitCode)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// ResolveWallpaper turns a checked wallpaper path into the file it names,
// following links but staying under the allowed places, and refusing
// anything but a regular file.
func (s *Session) ResolveWallpaper(p string) (string, error) {
	if err := CheckWallpaper(p); err != nil {
		return "", err
	}
	abs := p
	roots := []string{"/usr/share/backgrounds", "/usr/share/wallpapers"}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if s.cfg.Home == "" || !filepath.IsAbs(s.cfg.Home) {
			return "", errors.New("wallpaper: no home folder")
		}
		abs = filepath.Join(s.cfg.Home, rest)
		roots = []string{s.cfg.Home}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("wallpaper: %s is not there", p)
	}
	inside := false
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil && (real == rr || strings.HasPrefix(real, rr+"/")) {
			inside = true
		}
	}
	if !inside {
		return "", fmt.Errorf("wallpaper: %s leads outside the allowed places", p)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("wallpaper: %s is not a file", p)
	}
	return real, nil
}

// fileURI is a GVariant string holding the file URI of abs: every byte
// outside the URI's unreserved set is percent-encoded, so no quote,
// backslash or space can reach gsettings.
func fileURI(abs string) string {
	var b strings.Builder
	b.WriteString("'file://")
	for _, c := range []byte(abs) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("/._~-", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// Wallpaper is the GNOME wallpaper, as its two settings hold it.
type Wallpaper struct {
	URI     string `json:"uri"`      // GVariant string, quoted
	DarkURI string `json:"dark_uri"` // the same for the dark style
}

// File is the wallpaper's file path when URI is a file URI, or "".
func (w Wallpaper) File() string {
	u, err := url.Parse(strings.Trim(w.URI, "'"))
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return u.Path
}

// State is the session settings as they are now. A nil field is not
// supported or could not be read.
type State struct {
	Desktop      Desktop    `json:"desktop"`
	Wallpaper    *Wallpaper `json:"wallpaper,omitempty"`
	DoNotDisturb *bool      `json:"do_not_disturb,omitempty"`
	Backlight    string     `json:"backlight,omitempty"`
	Brightness   *int       `json:"brightness,omitempty"` // percent of Backlight
}

// Current reads the session settings.
func (s *Session) Current(ctx context.Context) State {
	st := State{Desktop: s.cfg.Desktop}
	if s.cfg.Desktop.GNOME {
		uri, err1 := s.gsettings(ctx, "get", schemaBackground, keyPicture)
		dark, err2 := s.gsettings(ctx, "get", schemaBackground, keyPictureDark)
		if err1 == nil && err2 == nil {
			st.Wallpaper = &Wallpaper{URI: uri, DarkURI: dark}
		}
		if v, err := s.gsettings(ctx, "get", schemaNotifications, keyBanners); err == nil && (v == "true" || v == "false") {
			dnd := v == "false"
			st.DoNotDisturb = &dnd
		}
	}
	if b, ok := s.PreferredBacklight(); ok {
		st.Backlight = b.Name
		st.Brightness = &b.Percent
	}
	return st
}

// SetWallpaperURIs sets both wallpaper settings to values read earlier by
// Current, to put a wallpaper back. Each must be a quoted file URI.
func (s *Session) SetWallpaperURIs(ctx context.Context, w Wallpaper) error {
	if !s.cfg.Desktop.GNOME {
		return errors.New("session: not a GNOME session")
	}
	for _, v := range []string{w.URI, w.DarkURI} {
		if err := safeexec.GSettingsFileURI(v); err != nil {
			return fmt.Errorf("session: wallpaper %s: %w", v, err)
		}
	}
	if _, err := s.gsettings(ctx, "set", schemaBackground, keyPicture, w.URI); err != nil {
		return err
	}
	_, err := s.gsettings(ctx, "set", schemaBackground, keyPictureDark, w.DarkURI)
	return err
}

// SetDoNotDisturb turns Do Not Disturb on or off (GNOME: banners off or on).
func (s *Session) SetDoNotDisturb(ctx context.Context, on bool) error {
	if !s.cfg.Desktop.GNOME {
		return errors.New("session: not a GNOME session")
	}
	_, err := s.gsettings(ctx, "set", schemaNotifications, keyBanners, strconv.FormatBool(!on))
	return err
}

// Backlight is one display backlight.
type Backlight struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // firmware, platform or raw
	Percent int    `json:"percent"`
}

var backlightNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// PreferredBacklight is the backlight a mode's brightness applies to:
// firmware before platform before raw, as GNOME picks, then by name.
func (s *Session) PreferredBacklight() (Backlight, bool) {
	entries, err := os.ReadDir(s.cfg.Backlight)
	if err != nil {
		return Backlight{}, false
	}
	rank := map[string]int{"firmware": 0, "platform": 1, "raw": 2}
	var best Backlight
	found := false
	for _, e := range entries { // sorted by name
		name := e.Name()
		if !backlightNameRe.MatchString(name) {
			continue
		}
		read := func(f string) (string, bool) {
			b, err := os.ReadFile(filepath.Join(s.cfg.Backlight, name, f)) // #nosec G304 -- name validated, under the sysfs class dir
			if err != nil || len(b) > 64 {
				return "", false
			}
			return strings.TrimSpace(string(b)), true
		}
		typ, ok1 := read("type")
		cur, ok2 := read("brightness")
		maxS, ok3 := read("max_brightness")
		v, err1 := strconv.Atoi(cur)
		m, err2 := strconv.Atoi(maxS)
		r, known := rank[typ]
		if !ok1 || !ok2 || !ok3 || err1 != nil || err2 != nil || m <= 0 || v < 0 || !known {
			continue
		}
		if found && rank[best.Type] <= r {
			continue
		}
		best, found = Backlight{Name: name, Type: typ, Percent: (v*100 + m/2) / m}, true
	}
	return best, found
}

func one(summary, id, name string) actions.Plan {
	return actions.Plan{Summary: summary, Items: []actions.Item{{ID: id, Name: name}}, Blocked: []actions.Blocked{}}
}

// blockDesktop is a plan refused because the desktop is not supported.
func (s *Session) blockDesktop(id, name string) actions.Plan {
	p := one(name, id, name)
	p.Items = []actions.Item{}
	p.Blocked = []actions.Blocked{{Item: actions.Item{ID: id, Name: name, Detail: s.cfg.Desktop.Name}, Reason: ReasonDesktop}}
	return p
}

// Register adds session/wallpaper (target: a wallpaper path),
// session/wallpaper-uris (both settings as read before) and session/dnd
// (target: on or off). All are safe: no preview token, and audited.
func (s *Session) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "session", Name: "wallpaper", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 {
				return errors.New("params must be empty")
			}
			return CheckWallpaper(r.Target)
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			if !s.cfg.Desktop.GNOME {
				return s.blockDesktop("wallpaper", "wallpaper"), nil
			}
			abs, err := s.ResolveWallpaper(r.Target)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "%v", err)
			}
			p := one("set the wallpaper to "+r.Target, "wallpaper", r.Target)
			p.State = abs
			return p, nil
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			abs, _ := p.State.(string)
			if abs == "" {
				return actions.Result{}, errors.New("session: no plan")
			}
			uri := fileURI(abs)
			return s.result(p, s.SetWallpaperURIs(ctx, Wallpaper{URI: uri, DarkURI: uri}))
		}})
	// Putting back a wallpaper read earlier (ROADMAP 10.5): both settings
	// as they were, which may differ between the light and dark styles.
	reg.Register(actions.Action{Kind: "session", Name: "wallpaper-uris", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if r.Target != "wallpaper" || len(r.Params) != 2 ||
				safeexec.GSettingsFileURI(r.Params["uri"]) != nil || safeexec.GSettingsFileURI(r.Params["dark_uri"]) != nil {
				return errors.New(`target must be "wallpaper" and params {"uri", "dark_uri"} quoted file URIs`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			if !s.cfg.Desktop.GNOME {
				return s.blockDesktop("wallpaper", "wallpaper"), nil
			}
			name := Wallpaper{URI: r.Params["uri"]}.File()
			return one("put the wallpaper back to "+name, "wallpaper", name), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.result(p, s.SetWallpaperURIs(ctx, Wallpaper{URI: r.Params["uri"], DarkURI: r.Params["dark_uri"]}))
		}})
	reg.Register(actions.Action{Kind: "session", Name: "dnd", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if (r.Target != "on" && r.Target != "off") || len(r.Params) != 0 {
				return errors.New(`target must be "on" or "off" and params empty`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			if !s.cfg.Desktop.GNOME {
				return s.blockDesktop("dnd", "Do Not Disturb"), nil
			}
			return one("turn Do Not Disturb "+r.Target, "dnd", "Do Not Disturb"), nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.result(p, s.SetDoNotDisturb(ctx, r.Target == "on"))
		}})
}

// result is one item done, or failed when err is not nil.
func (s *Session) result(p actions.Plan, err error) (actions.Result, error) {
	if err != nil {
		return actions.Result{Failed: []actions.Blocked{{Item: p.Items[0], Reason: "error"}}}, nil
	}
	return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
}

var drmConnectorRe = regexp.MustCompile(`^card[0-9]+-[A-Za-z0-9-]{1,32}$`)

// ConnectedDisplays counts the display connectors the kernel reports as
// connected, from <sys>/class/drm/card*-*/status (a mode's display
// trigger, ROADMAP 10.6). sys is "/sys" in production.
func ConnectedDisplays(sys string) (int, error) {
	dir := filepath.Join(sys, "class", "drm")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !drmConnectorRe.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "status")) // #nosec G304 -- name matched, under the sysfs class dir
		if err == nil && strings.TrimSpace(string(b)) == "connected" {
			n++
		}
	}
	return n, nil
}
