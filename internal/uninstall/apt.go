package uninstall

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/space"
)

// Orphans are computed by apt's own resolver, never by Hearth's: two
// read-only simulations of autoremove, before and with the program
// removed (docs/plan/uninstall.md §2.1).

// SimRemoval is one "Remv" line of apt-get -s.
type SimRemoval struct {
	Name    string `json:"name"`
	Arch    string `json:"arch,omitempty"` // "" for the native architecture
	Version string `json:"version,omitempty"`
}

// ID is "name:arch", with arch filled in for the native architecture.
func (s SimRemoval) ID(native string) string {
	if s.Arch == "" {
		return s.Name + ":" + native
	}
	return s.Name + ":" + s.Arch
}

// maxSimLines bounds the output read.
const maxSimLines = 20000

// remvRe is "Remv name[:arch] [version]" (the version may be missing for a
// half-installed package, and is followed by " [other]" when a newer one
// is held back).
var remvRe = regexp.MustCompile(`^Remv ([a-z0-9][a-z0-9+.-]{1,127})(?::([a-z0-9][a-z0-9-]{0,31}))?(?: \[([^\]\s]{1,128})\])?(?: \[[^\]]*\])?$`)

// ParseSim reads the removals of an apt-get -s run. A simulation that
// would install, upgrade or configure anything, or reports an error, is
// refused, since an uninstall only removes.
func ParseSim(out string) ([]SimRemoval, error) {
	lines := strings.Split(out, "\n")
	if len(lines) > maxSimLines {
		return nil, errors.New("apt-get printed too much")
	}
	var rm []SimRemoval
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "Remv "):
			m := remvRe.FindStringSubmatch(l)
			if m == nil {
				return nil, fmt.Errorf("apt-get printed a removal Hearth cannot read: %q", clipLine(l))
			}
			r := SimRemoval{Name: m[1], Arch: m[2], Version: m[3]}
			if k := r.Name + ":" + r.Arch; !seen[k] {
				seen[k] = true
				rm = append(rm, r)
			}
		case strings.HasPrefix(l, "Inst "), strings.HasPrefix(l, "Conf "), strings.HasPrefix(l, "Purg "):
			return nil, fmt.Errorf("the simulation would do more than remove: %q", clipLine(l))
		case strings.HasPrefix(l, "E: "):
			return nil, fmt.Errorf("apt-get: %s", clipLine(strings.TrimPrefix(l, "E: ")))
		}
	}
	return rm, nil
}

func clipLine(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// NativeArch is dpkg's name for the architecture Hearth runs on.
func NativeArch() string {
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

// Kept is a package left alone, and why.
type Kept struct {
	ID     string `json:"id"`
	Reason string `json:"reason"` // manual: installed by the user
}

// idOf names r as dpkg does: an "all" package has no native suffix.
func idOf(r SimRemoval, native string, byID map[string]discovery.Package) string {
	if r.Arch == "" {
		if _, ok := byID[r.Name+":all"]; ok {
			return r.Name + ":all"
		}
	}
	return r.ID(native)
}

// Orphans are the packages that become autoremovable only because the
// program goes: after − before − program − takenAlong, where takenAlong
// is what removing the program itself takes (its reverse dependencies,
// from PackageKit's simulation). Anything not marked automatically
// installed is dropped and kept, so a package the user installed is never
// touched. IDs are "name:arch", sorted.
func Orphans(before, after []SimRemoval, program, takenAlong []string, installed []discovery.Package, native string) (orphans []string, kept []Kept) {
	byID := make(map[string]discovery.Package, len(installed))
	for _, p := range installed {
		byID[p.ID()] = p
	}
	was := map[string]bool{}
	for _, r := range before {
		was[idOf(r, native, byID)] = true
	}
	skip := map[string]bool{}
	for _, id := range append(slices.Clone(program), takenAlong...) {
		skip[id] = true
	}
	for _, r := range after {
		id := idOf(r, native, byID)
		if was[id] || skip[id] || skip[r.Name] {
			continue
		}
		p, ok := byID[id]
		if !ok || p.State != "installed" {
			continue
		}
		if !p.Auto {
			kept = append(kept, Kept{ID: id, Reason: "manual"})
			continue
		}
		orphans = append(orphans, id)
	}
	sort.Strings(orphans)
	orphans = slices.Compact(orphans)
	return orphans, kept
}

// Reasons a package can never be in a plan (SECURITY S16, S16k).
const (
	BlockSelf      = "self"
	BlockEssential = "essential"
	BlockProtected = "protected"
	BlockRequired  = "required"
	BlockKernel    = "kernel"
)

var undeletable = map[string]string{
	"hearth": BlockSelf, "essential": BlockEssential, "protected": BlockProtected, "required": BlockRequired,
}

// Guard decides which packages may be removed, from dpkg's view and the
// running kernel's release.
type Guard struct {
	Installed []discovery.Package
	Running   string
}

// Blocked is why the package id ("name" or "name:arch") must never be
// removed, or "". It is asked of every package a simulation removes, not
// only the item's.
func (g Guard) Blocked(id string) string {
	name, arch, _ := strings.Cut(id, ":")
	if name == "hearth" {
		return BlockSelf
	}
	for _, p := range g.Installed {
		if p.Name == name && (arch == "" || p.Arch == arch || p.Arch == "all") {
			if why := p.Undeletable(); why != "" {
				return undeletable[why]
			}
		}
	}
	if space.KeptKernel(g.Installed, g.Running, name) {
		return BlockKernel
	}
	return ""
}

// desktopNames are packages whose removal can end the graphical session;
// a trailing "*" matches any suffix.
var desktopNames = []string{
	"gnome-shell", "gnome-session*", "mutter", "gdm3", "sddm", "lightdm", "plasma-workspace", "plasma-desktop",
	"kwin-*", "xfce4-session", "xfwm4", "cinnamon", "cinnamon-session", "muffin", "mate-session-manager", "marco",
	"xserver-xorg-core", "xserver-xorg", "xwayland", "xorg", "task-desktop", "task-gnome-desktop",
	"task-kde-desktop", "task-xfce-desktop", "task-cinnamon-desktop", "task-mate-desktop", "task-lxqt-desktop",
	"lxqt-session", "lxsession", "openbox", "sway", "weston", "labwc", "wayfire",
}

// desktopProvides are virtual packages that mark a display manager,
// session manager or window manager.
var desktopProvides = map[string]bool{"x-display-manager": true, "x-session-manager": true, "x-window-manager": true}

// Desktop reports whether removing the package id needs the double typed
// confirmation: it is on the fixed list, provides a display, session or
// window manager, or owns what runs the current session (session, by
// package name, from SessionOwners).
func (g Guard) Desktop(id string, session map[string]bool) bool {
	name, arch, _ := strings.Cut(id, ":")
	if session[name] {
		return true
	}
	for _, d := range desktopNames {
		if pre, ok := strings.CutSuffix(d, "*"); (ok && strings.HasPrefix(name, pre)) || d == name {
			return true
		}
	}
	for _, p := range g.Installed {
		if p.Name != name || (arch != "" && p.Arch != arch && p.Arch != "all") {
			continue
		}
		for _, v := range discovery.DepNames(p.Provides) {
			if desktopProvides[v] {
				return true
			}
		}
	}
	return false
}

// CheckTransaction refuses a PackageKit simulation that removes anything
// the plan does not hold: removed are its removals as "name:arch", plan
// the package IDs the user confirmed.
func CheckTransaction(removed []string, plan []string) error {
	ok := map[string]bool{}
	for _, id := range plan {
		ok[id] = true
	}
	var extra []string
	for _, id := range removed {
		if !ok[id] {
			extra = append(extra, id)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("the packages changed since the preview (%s would also be removed); preview again", strings.Join(extra, ", "))
	}
	return nil
}
