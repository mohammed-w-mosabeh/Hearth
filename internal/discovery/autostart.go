package discovery

// Autostart entries (Desktop Application Autostart Specification): the
// .desktop files in $XDG_CONFIG_HOME/autostart and $XDG_CONFIG_DIRS
// (/etc/xdg)/autostart. A user file with the same name overrides a
// system one, so a system entry is turned off by writing a user copy with
// Hidden=true; nothing is ever deleted. Entries marked NoDisplay (desktop
// components such as GNOME's settings daemons) are not listed: turning
// them off would break the session.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AutostartEntry is one listed autostart entry.
type AutostartEntry struct {
	ID      string `json:"id"` // file name without ".desktop"
	Name    string `json:"name"`
	Comment string `json:"comment,omitempty"`
	Enabled bool   `json:"enabled"`
	// Origin is "system" for an entry from /etc/xdg (perhaps overridden
	// by the user), "user" for one only in the user's directory.
	Origin string `json:"origin"`
}

// AutostartConfig says where the entries are.
type AutostartConfig struct {
	ConfigHome string   // $XDG_CONFIG_HOME, default ~/.config
	ConfigDirs []string // $XDG_CONFIG_DIRS, default /etc/xdg
	Desktops   []string // $XDG_CURRENT_DESKTOP split on ":"
	Apps       *Index   // for adding an application
}

// Autostart lists and changes autostart entries.
type Autostart struct{ cfg AutostartConfig }

// NewAutostart returns an Autostart; home fills in the defaults.
func NewAutostart(cfg AutostartConfig, home string) *Autostart {
	if cfg.ConfigHome == "" {
		cfg.ConfigHome = filepath.Join(home, ".config")
	}
	if len(cfg.ConfigDirs) == 0 {
		cfg.ConfigDirs = []string{"/etc/xdg"}
	}
	return &Autostart{cfg: cfg}
}

// ErrNoEntry is returned for an entry that is not listed.
var ErrNoEntry = errors.New("no such autostart entry")

func (a *Autostart) userDir() string { return filepath.Join(a.cfg.ConfigHome, "autostart") }

// systemFile is the first system file for id, or "".
func (a *Autostart) systemFile(id string) string {
	for _, d := range a.cfg.ConfigDirs {
		if p := filepath.Join(d, "autostart", id+".desktop"); regular(p) {
			return p
		}
	}
	return ""
}

// List returns the entries, sorted by name.
func (a *Autostart) List() []AutostartEntry {
	ids := map[string]bool{}
	for _, d := range append([]string{a.userDir()}, autostartDirs(a.cfg.ConfigDirs)...) {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if id, ok := strings.CutSuffix(e.Name(), ".desktop"); ok && ValidID(id) {
				ids[id] = true
			}
		}
	}
	out := []AutostartEntry{}
	for id := range ids {
		if e, ok := a.entry(id); ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if x != y {
			return x < y
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func autostartDirs(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = filepath.Join(d, "autostart")
	}
	return out
}

// entry reads id as the session would: the user file if there is one.
// Its name comes from the system file when the user file is a bare
// override ("Hidden=true" only).
func (a *Autostart) entry(id string) (AutostartEntry, bool) {
	sys := a.systemFile(id)
	user := filepath.Join(a.userDir(), id+".desktop")
	var u, s *entry
	if b, err := readLimited(user, maxDesktopFile); err == nil {
		if e, ok := parseDesktop(b); ok {
			u = &e
		}
	}
	if sys != "" {
		if b, err := readLimited(sys, maxDesktopFile); err == nil {
			if e, ok := parseDesktop(b); ok {
				s = &e
			}
		}
	}
	eff := u
	if eff == nil {
		eff = s
	}
	if eff == nil {
		return AutostartEntry{}, false
	}
	shown := *eff
	if s != nil {
		// A user override may carry only the keys it changes.
		if shown.Name == "" {
			shown.Name = s.Name
		}
		if shown.Comment == "" {
			shown.Comment = s.Comment
		}
		shown.NoDisplay = shown.NoDisplay || s.NoDisplay
		if len(shown.OnlyShowIn) == 0 {
			shown.OnlyShowIn = s.OnlyShowIn
		}
		if len(shown.NotShowIn) == 0 {
			shown.NotShowIn = s.NotShowIn
		}
	}
	if shown.Name == "" || shown.NoDisplay || !a.forDesktop(shown) {
		return AutostartEntry{}, false
	}
	origin := "user"
	if s != nil {
		origin = "system"
	}
	return AutostartEntry{ID: id, Name: shown.Name, Comment: shown.Comment, Enabled: !shown.Hidden && !shown.AutostartOff, Origin: origin}, true
}

func (a *Autostart) forDesktop(e entry) bool {
	if len(a.cfg.Desktops) == 0 {
		return true
	}
	if len(e.OnlyShowIn) > 0 && !overlaps(e.OnlyShowIn, a.cfg.Desktops) {
		return false
	}
	return !overlaps(e.NotShowIn, a.cfg.Desktops)
}

// Set turns entry id on or off by writing the user's copy.
func (a *Autostart) Set(id string, enabled bool) error {
	if !ValidID(id) {
		return ErrNoEntry
	}
	if _, ok := a.entry(id); !ok {
		return ErrNoEntry
	}
	user := filepath.Join(a.userDir(), id+".desktop")
	// The user's file replaces the system one entirely, so a bare
	// override ("Hidden=true" alone) is rebuilt from the system file.
	src := user
	if b, err := readLimited(user, maxDesktopFile); err != nil {
		src = a.systemFile(id)
	} else if e, ok := parseDesktop(b); (!ok || e.Exec == "") && a.systemFile(id) != "" {
		src = a.systemFile(id)
	}
	b, err := readLimited(src, maxDesktopFile)
	if err != nil {
		return err
	}
	hidden := "true"
	if enabled {
		hidden = "false"
	}
	keys := map[string]string{"Hidden": hidden}
	if enabled {
		keys["X-GNOME-Autostart-enabled"] = "true" // only rewritten where present
	}
	return a.write(user, setDesktopKeys(b, keys, map[string]bool{"X-GNOME-Autostart-enabled": true}))
}

// Add makes application appID start at login, from its .desktop file.
// An app already listed is turned on instead.
func (a *Autostart) Add(appID string) error {
	if a.cfg.Apps == nil || !ValidID(appID) {
		return ErrNoEntry
	}
	app, ok := a.cfg.Apps.App(appID)
	if !ok || app.Source == SourceAppImage || !strings.HasSuffix(app.Path, ".desktop") {
		return fmt.Errorf("%w: only installed applications can start at login", ErrNoEntry)
	}
	if _, listed := a.entry(appID); listed {
		return a.Set(appID, true)
	}
	b, err := readLimited(app.Path, maxDesktopFile)
	if err != nil {
		return err
	}
	return a.write(filepath.Join(a.userDir(), appID+".desktop"), setDesktopKeys(b, map[string]string{"Hidden": "false"}, nil))
}

// write replaces p atomically: a temporary file in the same directory,
// synced, then renamed over p (a symlink at p is replaced, not followed).
func (a *Autostart) write(p string, b []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hearth-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after the rename
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// setDesktopKeys sets keys in the [Desktop Entry] group of a .desktop
// file and leaves every other line as it was. A key already in the
// group is rewritten in place; a missing one is added after the group
// header, unless onlyIfPresent names it.
func setDesktopKeys(b []byte, keys map[string]string, onlyIfPresent map[string]bool) []byte {
	var out bytes.Buffer
	done := map[string]bool{}
	// Without a [Desktop Entry] header every line ends up in the group
	// prepended below, so every line is edited as part of it.
	in := !hasEntryGroup(b)
	header := -1 // offset in out just after the header line
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), maxDesktopFile)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = t == "[Desktop Entry]"
			out.WriteString(line + "\n")
			if in && header < 0 {
				header = out.Len()
			}
			continue
		}
		if in {
			if k, _, ok := strings.Cut(t, "="); ok {
				k = strings.TrimSpace(k)
				if v, want := keys[k]; want {
					if !done[k] {
						out.WriteString(k + "=" + v + "\n")
					}
					done[k] = true // duplicates of the key are dropped
					continue
				}
			}
		}
		out.WriteString(line + "\n")
	}
	var add []string
	for k, v := range keys {
		if !done[k] && !onlyIfPresent[k] {
			add = append(add, k+"="+v+"\n")
		}
	}
	sort.Strings(add)
	res := out.Bytes()
	if header < 0 {
		return append([]byte("[Desktop Entry]\n"+strings.Join(add, "")), res...)
	}
	return append(append(append([]byte{}, res[:header]...), strings.Join(add, "")...), res[header:]...)
}

func hasEntryGroup(b []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), maxDesktopFile)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "[Desktop Entry]" {
			return true
		}
	}
	return false
}
