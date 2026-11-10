package uninstall

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Identifiers are the names an item is known by, collected by the
// resolver. Leftovers are matched against them (docs/plan/uninstall.md
// §2.4, §2.5). Values come from untrusted files (desktop entries, package
// lists) and are only ever compared, never used as paths.
type Identifiers struct {
	Packages []string `json:"packages,omitempty"` // dpkg or another manager's package names
	Desktop  []string `json:"desktop,omitempty"`  // desktop file IDs without ".desktop"
	Exec     []string `json:"exec,omitempty"`     // program names
	WMClass  []string `json:"wm_class,omitempty"` // StartupWMClass
	AppIDs   []string `json:"app_ids,omitempty"`  // Flatpak and reverse-DNS IDs: exact
	Snaps    []string `json:"snaps,omitempty"`    // snap names: exact
	Names    []string `json:"names,omitempty"`    // manual install folder names
}

// maxIdent bounds one identifier; longer ones are dropped.
const maxIdent = 255

// identRe is what an identifier may hold to be kept at all.
var identRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+@ ()~-]*$`)

// stoplist are names too generic to guess a folder by: a program called
// "data" does not own ~/.config/data.
var stoplist = set("share", "data", "cache", "config", "lib", "bin", "sbin", "tmp", "temp", "local", "default",
	"common", "user", "users", "system", "app", "apps", "application", "env", "sh", "bash", "sudo", "flatpak",
	"snap", "python", "python3", "java", "node", "electron", "wine", "run", "start", "launcher", "main", "desktop",
	"settings", "profile", "home", "files", "logs", "log", "state", "var", "opt", "etc", "usr", "test", "debug")

// reverseDNS is an ID with at least three parts, as Flatpak IDs are.
var reverseDNS = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+){2,}$`)

// add appends the usable values of vs to dst, without duplicates.
func add(dst []string, vs ...string) []string {
	for _, v := range vs {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > maxIdent || !identRe.MatchString(v) || slices.Contains(dst, v) {
			continue
		}
		dst = append(dst, v)
	}
	return dst
}

// Clean drops unusable and duplicate values, so an Identifiers built by
// hand is safe to match with.
func (ids Identifiers) Clean() Identifiers {
	return Identifiers{
		Packages: add(nil, ids.Packages...), Desktop: add(nil, ids.Desktop...), Exec: add(nil, ids.Exec...),
		WMClass: add(nil, ids.WMClass...), AppIDs: add(nil, ids.AppIDs...), Snaps: add(nil, ids.Snaps...),
		Names: add(nil, ids.Names...),
	}
}

// Merge adds o's values to ids.
func (ids Identifiers) Merge(o Identifiers) Identifiers {
	return Identifiers{
		Packages: add(ids.Packages, o.Packages...), Desktop: add(ids.Desktop, o.Desktop...),
		Exec: add(ids.Exec, o.Exec...), WMClass: add(ids.WMClass, o.WMClass...),
		AppIDs: add(ids.AppIDs, o.AppIDs...), Snaps: add(ids.Snaps, o.Snaps...), Names: add(ids.Names, o.Names...),
	}
}

// Match says why a leftover was matched, and whether it starts checked.
type Match struct {
	Preselect bool   `json:"preselect"`
	By        string `json:"by"`    // package, desktop, exec, wm_class, app_id, snap, name
	Value     string `json:"value"` // the identifier it matched
	Reason    string `json:"reason"`
}

// fold is how names compare when guessing: case and a leading dot do not
// count.
func fold(s string) string { return strings.ToLower(strings.TrimPrefix(s, ".")) }

// guessable is a folded name long and specific enough to guess by.
func guessable(f string) bool { return len(f) >= 3 && !stoplist[f] }

// MatchUser matches name, a direct child of the user root root (as
// CheckUser returns them), against ids. An exact app ID, snap name or
// reverse-DNS desktop ID is preselected; a name that only looks like the
// package, program, desktop ID or window class is not, and says so.
func (ids Identifiers) MatchUser(root, name string) (Match, bool) {
	switch root {
	case ".var/app":
		// Flatpak's own folder per app: the ID, exactly, or nothing.
		if slices.Contains(ids.AppIDs, name) {
			return Match{true, "app_id", name, fmt.Sprintf("the Flatpak app %s keeps its data here", name)}, true
		}
		return Match{}, false
	case "snap":
		if slices.Contains(ids.Snaps, name) {
			return Match{true, "snap", name, fmt.Sprintf("the snap %s keeps its data here", name)}, true
		}
		return Match{}, false
	}
	bare := strings.TrimPrefix(name, ".")
	if root == "" && bare == name {
		return Match{}, false // only dot-folders are taken from the home itself
	}
	for _, v := range ids.AppIDs {
		if bare == v {
			return Match{true, "app_id", v, fmt.Sprintf("named exactly like the app ID %s", v)}, true
		}
	}
	for _, v := range ids.Desktop {
		if bare == v && reverseDNS.MatchString(v) {
			return Match{true, "desktop", v, fmt.Sprintf("named exactly like the app ID %s", v)}, true
		}
	}
	f := fold(name)
	if !guessable(f) {
		return Match{}, false
	}
	for _, c := range []struct {
		by, what string
		vs       []string
	}{
		{"package", "the package", ids.Packages},
		{"exec", "the program", ids.Exec},
		{"name", "the install folder", ids.Names},
		{"desktop", "the desktop entry", ids.Desktop},
		{"wm_class", "its window class", ids.WMClass},
		{"snap", "the snap", ids.Snaps},
		{"app_id", "the app ID", ids.AppIDs},
	} {
		for _, v := range c.vs {
			if fold(v) == f {
				return Match{false, c.by, v, fmt.Sprintf("named like %s %s", c.what, v)}, true
			}
		}
	}
	return Match{}, false
}

// MatchSystem matches name, a direct child of the system root root, against
// ids. Only exact names count (package, program, install folder), and a
// system leftover never starts checked.
func (ids Identifiers) MatchSystem(root, name string) (Match, bool) {
	if !guessable(fold(name)) {
		return Match{}, false
	}
	cands := []struct {
		by, what string
		vs       []string
	}{
		{"package", "the package", ids.Packages},
		{"exec", "the program", ids.Exec},
		{"name", "the install folder", ids.Names},
	}
	if root == "/usr/local/bin" {
		cands = cands[1:2] // a program, by its own name
	}
	for _, c := range cands {
		if slices.Contains(c.vs, name) {
			return Match{false, c.by, name, fmt.Sprintf("named exactly like %s %s", c.what, name)}, true
		}
	}
	return Match{}, false
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
