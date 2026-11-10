package uninstall

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Running parts: the user's processes of the app, its systemd units and
// its autostart entries (docs/plan/uninstall.md §2.3).

// Running finds an item's running parts.
type Running struct {
	Local
	// Procs returns the processes of the groups ids (collect's
	// LookupGroups); Self is hearthd's pid.
	Procs func(ids []string) ([]collect.ProcRef, error)
	Self  int
	// UnitState reads a unit's active and enablement states; ok is false
	// when the manager does not know it.
	UnitState func(user bool, name string) (active, enabled string, ok bool)
	// PackageFiles lists an installed package's files ("name:arch").
	PackageFiles func(id string) []string
	// EtcUnits is /etc/systemd/system, where an administrator's own units
	// live.
	EtcUnits string
}

// unitDirs are where packages ship system units.
var unitDirs = map[string]bool{"/usr/lib/systemd/system": true, "/lib/systemd/system": true}

// unitSuffixes are the units Hearth stops and disables.
var unitSuffixes = []string{".service", ".socket", ".timer", ".path"}

func isUnit(name string) bool {
	if safeexec.UnitName(name) != nil || strings.Contains(name, "@.") {
		return false // templates: their instances are not known here
	}
	for _, s := range unitSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// removedPaths are the program's own files: every candidate path of the
// program group, and the programs its packages ship.
func removedPaths(program []Candidate, files func(string) []string) []string {
	var out []string
	for _, c := range program {
		switch c.Kind {
		case KindTrash, KindArchive:
			out = append(out, c.ID)
		case KindPackage:
			if files == nil {
				continue
			}
			for _, f := range files(c.ID) {
				if programDirs[path.Dir(f)] || strings.HasPrefix(f, "/opt/") || strings.HasPrefix(f, "/usr/lib/") {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// inside reports whether p is one of roots or below one.
func inside(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// Parts lists the running parts of res, given its program candidates.
func (r Running) Parts(res Resolved, program []Candidate) []Candidate {
	removed := removedPaths(program, r.PackageFiles)
	var out []Candidate
	out = append(out, r.processes(res, removed)...)
	out = append(out, r.systemUnits(res, program, removed)...)
	out = append(out, r.userUnits(res, removed)...)
	out = append(out, r.autostart(res, removed)...)
	return out
}

func (r Running) processes(res Resolved, removed []string) []Candidate {
	if r.Procs == nil {
		return nil
	}
	var groups []string
	for _, id := range slices.Concat(res.Identifiers.Desktop, res.Identifiers.AppIDs) {
		groups = append(groups, "app:"+id)
	}
	for _, p := range removed {
		groups = append(groups, "exe:"+p)
	}
	if len(groups) == 0 {
		return nil
	}
	refs, err := r.Procs(groups)
	if err != nil {
		return []Candidate{remains("procs", "running processes", "they could not be read: "+err.Error())}
	}
	var out []Candidate
	for _, p := range refs {
		c := Candidate{Group: GroupRunning, Kind: KindProcess, ID: fmt.Sprintf("pid:%d:%d", p.PID, p.Start),
			Name: p.Name, Preselect: true, Reason: "running; asked to quit, then stopped if it does not"}
		switch {
		case p.PID == r.Self || p.PID == 1:
			continue
		case !p.Own:
			c.Group, c.Kind, c.Preselect = GroupRemains, KindNothing, false
			c.Reason = fmt.Sprintf("runs as %s: stopped only with its service, otherwise the uninstall waits", p.User)
			c.Warn = "blocks"
		case strings.Contains(p.Cgroup, "/session.slice/"):
			c.Group, c.Kind, c.Preselect = GroupRemains, KindNothing, false
			c.Reason = "part of your desktop session; never stopped"
			c.Warn = "blocks"
		}
		out = append(out, c)
	}
	return out
}

// unit is a candidate for a unit to stop and disable, if it is loaded and
// active or enabled.
func (r Running) unit(user bool, name, reason string) (Candidate, bool) {
	scope := "system"
	if user {
		scope = "user"
	}
	c := Candidate{Group: GroupRunning, Kind: KindUnit, ID: scope + "/" + name, Name: name, Preselect: true, Reason: reason}
	if r.UnitState == nil {
		return c, true
	}
	active, enabled, ok := r.UnitState(user, name)
	if !ok || (active != "active" && active != "activating" && !strings.HasPrefix(enabled, "enabled")) {
		return c, false
	}
	return c, true
}

// execStart is the program of a unit file's first ExecStart=, without
// systemd's prefixes.
func execStart(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		v, ok := strings.CutPrefix(l, "ExecStart=")
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		return strings.TrimLeft(firstField(v), "@-:+!|")
	}
	return ""
}

// readSmall reads a regular file of at most 64 KiB, not following a link.
func readSmall(p string) ([]byte, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return nil, false
	}
	f, err := os.Open(p) // #nosec G304 -- a unit or desktop file in a fixed folder
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	return b, err == nil
}

// matchesName reports whether a unit or desktop file's name (without its
// suffix) is exactly one of the item's names.
func matchesName(base string, ids Identifiers) bool {
	return slices.Contains(ids.Packages, base) || slices.Contains(ids.Exec, base) || slices.Contains(ids.Names, base) ||
		slices.Contains(ids.Desktop, base) || slices.Contains(ids.AppIDs, base) || slices.Contains(ids.Snaps, base)
}

func (r Running) systemUnits(res Resolved, program []Candidate, removed []string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	if r.PackageFiles != nil {
		for _, c := range program {
			if c.Kind != KindPackage {
				continue
			}
			for _, f := range r.PackageFiles(c.ID) {
				name := path.Base(f)
				if !unitDirs[path.Dir(f)] || !isUnit(name) || seen[name] {
					continue
				}
				seen[name] = true
				if u, ok := r.unit(false, name, "a service of "+c.ID); ok {
					out = append(out, u)
				}
			}
		}
	}
	if r.EtcUnits == "" {
		return out
	}
	ents, err := os.ReadDir(r.EtcUnits)
	if err != nil {
		return out
	}
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !isUnit(name) || seen[name] {
			continue
		}
		base := name[:strings.LastIndexByte(name, '.')]
		if !matchesName(base, res.Identifiers) {
			continue
		}
		b, ok := readSmall(filepath.Join(r.EtcUnits, name))
		if !ok || !inside(execStart(b), removed) {
			continue
		}
		seen[name] = true
		if u, ok := r.unit(false, name, "an administrator's service that starts this program"); ok {
			out = append(out, u)
		}
		out = append(out, remains(filepath.Join(r.EtcUnits, name), filepath.Join(r.EtcUnits, name),
			"the unit file stays in /etc/systemd/system, disabled"))
	}
	return out
}

func (r Running) userUnits(res Resolved, removed []string) []Candidate {
	dir := filepath.Join(r.Home, ".config/systemd/user")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !isUnit(name) {
			continue
		}
		base := name[:strings.LastIndexByte(name, '.')]
		b, ok := readSmall(filepath.Join(dir, name))
		if !ok || !matchesName(base, res.Identifiers) || !inside(execStart(b), removed) {
			continue
		}
		if u, ok := r.unit(true, name, "your own service that starts this program"); ok {
			out = append(out, u)
		}
		if c, ok := pathCandidate(GroupRunning, KindTrash, filepath.Join(dir, name), "the unit file of that service", true); ok {
			out = append(out, c)
		}
		if c, ok := pathCandidate(GroupRunning, KindTrash, filepath.Join(dir, name+".d"), "its drop-in folder", true); ok {
			out = append(out, c)
		}
	}
	return out
}

func (r Running) autostart(res Resolved, removed []string) []Candidate {
	dir := filepath.Join(r.Home, ".config/autostart")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".desktop") {
			continue
		}
		b, ok := readSmall(filepath.Join(dir, name))
		if !ok {
			continue
		}
		exec, _ := desktopKeys(b)
		prog := strings.Trim(firstField(exec), `"'`)
		id := strings.TrimSuffix(name, ".desktop")
		reason := ""
		switch {
		case slices.Contains(res.Identifiers.Desktop, id) || slices.Contains(res.Identifiers.AppIDs, id):
			reason = "starts this app when you log in"
		case filepath.IsAbs(prog) && inside(prog, removed):
			reason = "starts this program when you log in"
		case !strings.Contains(prog, "/") && slices.Contains(res.Identifiers.Exec, prog) && guessable(fold(prog)):
			reason = "starts the program " + prog + " when you log in"
		default:
			continue
		}
		if c, ok := pathCandidate(GroupRunning, KindTrash, filepath.Join(dir, name), reason, true); ok {
			out = append(out, c)
		}
	}
	return out
}
