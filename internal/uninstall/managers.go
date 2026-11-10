package uninstall

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Program candidates: what removes the program itself, by its own manager
// (docs/plan/uninstall.md §2.2).

// Local is what program detection needs to know about the machine.
type Local struct {
	Home string
	UID  uint32
	// InstallDir is a Lutris or Heroic game's folder (discovery.Games).
	InstallDir func(id string) (string, bool)
	// SteamLibraries lists the Steam libraries (discovery.SteamLibraries).
	SteamLibraries func() []string
}

// Program lists the program group of res, plus what stays when the
// program cannot be removed by Hearth (in GroupRemains).
func (l Local) Program(res Resolved) []Candidate {
	o := res.Owner
	pkg := func(kind, id, name string) Candidate {
		return Candidate{Group: GroupProgram, Kind: kind, ID: id, Name: name, Preselect: true,
			Reason: fmt.Sprintf("removed by %s", managerName[o.Manager])}
	}
	var out []Candidate
	switch o.Manager {
	case ManagerApt:
		for _, id := range o.Packages {
			out = append(out, pkg(KindPackage, id, id))
		}
	case ManagerFlatpak:
		out = append(out, pkg(KindFlatpak, o.Target, o.Name))
	case ManagerSnap:
		c := pkg(KindSnap, o.Target, o.Name)
		c.Reason = "removed by snapd, which keeps a snapshot of its data for undo"
		out = append(out, c)
	case ManagerNpm, ManagerCargo, ManagerGoBin:
		out = append(out, pkg(KindExtra, o.Manager+":"+o.Target, o.Name))
	case ManagerPip:
		out = append(out, Candidate{Group: GroupRemains, Kind: KindManual, ID: o.Target,
			Name:   "pip uninstall --user " + o.Name,
			Reason: "pip packages are never removed by Hearth; run this command yourself"})
	case ManagerAppImage:
		for _, p := range o.Paths {
			out = append(out, l.userFile(p, "the AppImage itself"))
		}
	case ManagerManual:
		for _, p := range o.Paths {
			out = append(out, l.manual(p))
		}
	case ManagerSteam:
		out = append(out, Candidate{Group: GroupProgram, Kind: KindSteam, ID: o.Target, Name: o.Name, Preselect: true,
			Reason: "Steam removes its own games: its uninstall dialog opens"})
	case ManagerLutris, ManagerHeroic:
		out = append(out, l.gameDir(res))
	}
	return out
}

var managerName = map[string]string{
	ManagerApt: "apt (PackageKit)", ManagerFlatpak: "Flatpak", ManagerSnap: "snapd", ManagerNpm: "npm",
	ManagerCargo: "cargo", ManagerGoBin: "moving it to the trash",
}

func remains(id, name, reason string) Candidate {
	return Candidate{Group: GroupRemains, Kind: KindNothing, ID: id, Name: name, Reason: reason}
}

// userFile is a file of the user's own, under the home, to the trash.
func (l Local) userFile(p, reason string) Candidate {
	if !under(l.Home, p) {
		return remains(p, p, "outside your home folder")
	}
	uid, _, err := owner(p)
	if err != nil {
		return remains(p, p, "already gone")
	}
	if uid != l.UID {
		return remains(p, p, "not yours")
	}
	c, _ := pathCandidate(GroupProgram, KindTrash, p, reason, true)
	return c
}

// under reports whether p is strictly inside dir.
func under(dir, p string) bool {
	return dir != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && strings.HasPrefix(p, dir+"/")
}

// manual is one path of a manual install: the user's own goes to the
// trash, root's in a folder only root can write is archived by the broker,
// anything else stays.
func (l Local) manual(p string) Candidate {
	if under(l.Home, p) {
		rel := strings.TrimPrefix(p, l.Home+"/")
		dir, base := path.Split(rel)
		if (dir != ".local/opt/" && dir != "Applications/") || !systemNameRe.MatchString(base) {
			return remains(p, p, "not a folder Hearth removes")
		}
		return l.userFile(p, "the program's own folder")
	}
	if _, _, err := CheckSystem(p); err != nil {
		return remains(p, p, err.Error())
	}
	uid, safe, err := owner(p)
	switch {
	case err != nil:
		return remains(p, p, "already gone")
	case uid == l.UID:
		c, _ := pathCandidate(GroupProgram, KindTrash, p, "the program's own folder, yours", true)
		return c
	case uid != 0:
		return remains(p, p, "owned by another user")
	case !safe:
		return remains(p, p, "its folder can be written by someone other than root")
	}
	c, _ := pathCandidate(GroupProgram, KindArchive, p, "the program's own folder; archived for 7 days", true)
	return c
}

// xdgDirs are the home's own folders, never a game's folder.
var xdgDirs = set("Desktop", "Documents", "Downloads", "Music", "Pictures", "Public", "Templates", "Videos", "Games", "Applications")

// alnum folds s to its lower-case letters and digits.
func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, s)
}

// gameDir is a Lutris or Heroic game's folder, from the launcher's own
// record. The record is untrusted: the folder must be the user's, under
// the home, at least two levels down, a folder and not a link, and never
// one of the home's own folders. It starts checked only when its name
// matches the game's.
func (l Local) gameDir(res Resolved) Candidate {
	name := res.Owner.Name
	dir, ok := "", false
	if l.InstallDir != nil {
		dir, ok = l.InstallDir(res.Owner.Target)
	}
	note := fmt.Sprintf("%s still lists it until you remove it there", managerTitle[res.Owner.Manager])
	if !ok {
		return remains(res.Owner.Target, name, "the launcher records no folder; "+note)
	}
	if !under(l.Home, dir) || strings.Count(strings.TrimPrefix(dir, l.Home+"/"), "/") < 1 {
		return remains(dir, dir, "not a folder two levels inside your home folder; "+note)
	}
	if first, _, _ := strings.Cut(strings.TrimPrefix(dir, l.Home+"/"), "/"); strings.HasPrefix(first, ".") && first != ".local" {
		return remains(dir, dir, "inside a hidden folder of your home; "+note)
	}
	if xdgDirs[path.Base(dir)] {
		return remains(dir, dir, "one of your home's own folders")
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return remains(dir, dir, "not a folder (or already gone)")
	}
	if uid, _, err := owner(dir); err != nil || uid != l.UID {
		return remains(dir, dir, "not yours")
	}
	b := alnum(path.Base(dir))
	pre := len(b) >= 3 && (strings.Contains(alnum(name), b) || strings.Contains(b, alnum(name)))
	reason := "the game's folder, as " + managerTitle[res.Owner.Manager] + " records it; " + note
	if !pre {
		reason = "the folder " + managerTitle[res.Owner.Manager] + " records, though its name differs from the game's; " + note
	}
	c, _ := pathCandidate(GroupProgram, KindTrash, dir, reason, pre)
	return c
}

var managerTitle = map[string]string{ManagerLutris: "Lutris", ManagerHeroic: "Heroic"}

// steamAppID is a Steam app ID.
var steamAppID = regexp.MustCompile(`^[0-9]{1,10}$`)

// SteamLeftovers are a Steam game's Proton prefix and shader cache in each
// library, by exact app ID, preselected.
func (l Local) SteamLeftovers(appid string) []Candidate {
	if !steamAppID.MatchString(appid) || l.SteamLibraries == nil {
		return nil
	}
	var out []Candidate
	for _, lib := range l.SteamLibraries() {
		if !filepath.IsAbs(lib) || filepath.Clean(lib) != lib {
			continue
		}
		for _, sub := range []string{"compatdata", "shadercache"} {
			p := filepath.Join(lib, "steamapps", sub, appid)
			uid, _, err := owner(p)
			if err != nil || uid != l.UID {
				continue
			}
			if c, ok := pathCandidate(GroupUser, KindTrash, p, fmt.Sprintf("Steam's %s for app %s", sub, appid), true); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// FlatpakFreed are the runtimes and extensions only the app ref uses, so
// that removing it leaves them unused; offered unchecked. Runtimes used by
// any other app, and those already unused, are not offered. The app's own
// Locale and Debug extensions go with it by themselves.
func FlatpakFreed(list discovery.ExtraList, appRef string) []Candidate {
	f := strings.Split(appRef, "/")
	if len(f) != 5 || f[1] != "app" {
		return nil
	}
	id, arch := f[2], f[3]
	var out []Candidate
	for _, p := range list.Packages {
		if p.Kind != "runtime" {
			continue
		}
		rf := strings.Split(p.ID, "/")
		if len(rf) != 5 || rf[3] != arch {
			continue
		}
		own := strings.HasPrefix(rf[2], id+".")
		if own && (strings.HasSuffix(rf[2], ".Locale") || strings.HasSuffix(rf[2], ".Debug")) {
			continue
		}
		if !own && !slices.Equal(p.UsedBy, []string{id}) {
			continue
		}
		reason := "a runtime only this app uses"
		if own {
			reason = "an extension of this app"
		}
		out = append(out, Candidate{Group: GroupOrphans, Kind: KindFlatpak, ID: p.ID, Name: p.Name, Size: p.SizeBytes, Reason: reason})
	}
	return out
}

// maxDesktopScan bounds the desktop files read for an AppImage.
const (
	maxDesktopScan = 2000
	maxDesktopRead = 256 << 10
	maxIconScan    = 20000
)

// desktopKeys reads Exec= and Icon= of a desktop file's main group.
func desktopKeys(b []byte) (exec, icon string) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), maxDesktopRead)
	in := false
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			in = l == "[Desktop Entry]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Exec":
			exec = strings.TrimSpace(v)
		case "Icon":
			icon = strings.TrimSpace(v)
		}
	}
	return exec, icon
}

// iconExts are the icon files an integration writes.
var iconExts = set(".png", ".svg", ".svgz", ".xpm")

// AppImageIntegration are the desktop entries in ~/.local/share/applications
// that start the AppImage at img exactly, preselected, and the icons they
// name in ~/.local/share/icons: preselected when the name is
// AppImageLauncher's ("appimagekit_…") or the AppImage's own, otherwise
// unchecked. Links are never followed.
func (l Local) AppImageIntegration(img string) []Candidate {
	appDir := filepath.Join(l.Home, ".local/share/applications")
	root, err := os.OpenRoot(appDir)
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()
	ents, err := os.ReadDir(appDir)
	if err != nil {
		return nil
	}
	var out []Candidate
	icons := map[string]bool{}
	for i, e := range ents {
		if i >= maxDesktopScan {
			break
		}
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".desktop") {
			continue
		}
		f, err := root.Open(e.Name())
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, maxDesktopRead+1))
		_ = f.Close()
		if err != nil || len(b) > maxDesktopRead {
			continue
		}
		exec, icon := desktopKeys(b)
		if strings.Trim(firstField(exec), `"'`) != img {
			continue
		}
		if c, ok := pathCandidate(GroupUser, KindTrash, filepath.Join(appDir, e.Name()), "the desktop entry made for this AppImage", true); ok {
			out = append(out, c)
		}
		if icon != "" {
			icons[icon] = true
		}
	}
	if len(icons) == 0 {
		return out
	}
	self := strings.TrimSuffix(path.Base(img), path.Ext(img))
	iconDir := filepath.Join(l.Home, ".local/share/icons")
	n := 0
	_ = filepath.WalkDir(iconDir, func(p string, d os.DirEntry, err error) error {
		n++
		if err != nil || n > maxIconScan {
			return filepath.SkipDir
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, iconDir), "/") > 4 {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || !iconExts[path.Ext(p)] {
			return nil
		}
		base := strings.TrimSuffix(d.Name(), path.Ext(d.Name()))
		if !icons[base] && !icons[p] {
			return nil
		}
		pre := strings.HasPrefix(base, "appimagekit_") || base == self
		reason := "the icon its desktop entry names"
		if !pre {
			reason += "; another app could use the same name"
		}
		if c, ok := pathCandidate(GroupUser, KindTrash, p, reason, pre); ok {
			out = append(out, c)
		}
		return nil
	})
	return out
}
