package discovery

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxVDFFile bounds a Steam file; localconfig.vdf of a big library is a
// few MiB.
const maxVDFFile = 16 << 20

// steamRoots are the places a Steam client keeps its data: the Debian
// package (~/.steam/steam links to ~/.local/share/Steam), Flatpak, Snap.
func steamRoots(home string) []string {
	cands := []string{
		filepath.Join(home, ".steam/steam"),
		filepath.Join(home, ".local/share/Steam"),
		filepath.Join(home, ".var/app/com.valvesoftware.Steam/.local/share/Steam"),
		filepath.Join(home, "snap/steam/common/.local/share/Steam"),
	}
	return uniqueDirs(cands)
}

// uniqueDirs keeps the existing directories, once each after symlinks.
func uniqueDirs(cands []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() || seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out
}

// steamTool matches the runtimes and compatibility tools Steam installs
// like games.
var steamTool = regexp.MustCompile(`^(Proton\b|Steam Linux Runtime|Steamworks Common Redistributables|Steam Runtime)`)

// stateFullyInstalled is the StateFlags bit of a playable install.
const stateFullyInstalled = 4

var acfName = regexp.MustCompile(`^appmanifest_[0-9]{1,10}\.acf$`)

func steamGames(home string) []Game {
	var out []Game
	seen := map[string]bool{}
	for _, root := range steamRoots(home) {
		played := steamPlaytime(root)
		for _, lib := range steamLibraries(root) {
			dir := filepath.Join(lib, "steamapps")
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !acfName.MatchString(e.Name()) {
					continue
				}
				b, err := readLimited(filepath.Join(dir, e.Name()), maxDesktopFile)
				if err != nil {
					continue
				}
				gm, ok := steamManifest(string(b))
				if !ok || seen[gm.ID] {
					continue
				}
				seen[gm.ID] = true
				appid := strings.TrimPrefix(gm.ID, "steam.")
				if p, ok := played[appid]; ok {
					gm.Playtime = p.minutes * 60
					gm.LastPlayed = max(gm.LastPlayed, p.last)
				}
				gm.cover = steamCover(root, appid)
				gm.url = "steam://rungameid/" + appid
				out = append(out, gm)
			}
		}
	}
	return out
}

// steamManifest reads an appmanifest_<id>.acf.
func steamManifest(s string) (Game, bool) {
	root, err := parseVDF(s)
	if err != nil {
		return Game{}, false
	}
	st := root.Get("AppState")
	appid, name := st.Str("appid"), st.Str("name")
	if _, err := strconv.ParseUint(appid, 10, 32); err != nil || name == "" || steamTool.MatchString(name) {
		return Game{}, false
	}
	if flags, _ := strconv.ParseUint(st.Str("StateFlags"), 10, 32); flags&stateFullyInstalled == 0 {
		return Game{}, false
	}
	size, _ := strconv.ParseInt(st.Str("SizeOnDisk"), 10, 64)
	last, _ := strconv.ParseInt(st.Str("LastPlayed"), 10, 64)
	return Game{ID: "steam." + appid, Name: name, Source: GameSteam, Size: max(size, 0), LastPlayed: max(last, 0)}, true
}

// SteamLibraries are every Steam library of home: each Steam
// installation and the library folders it lists.
func SteamLibraries(home string) []string {
	var libs []string
	for _, root := range steamRoots(home) {
		libs = append(libs, steamLibraries(root)...)
	}
	return uniqueDirs(libs)
}

// steamLibraries are root and the library folders libraryfolders.vdf lists.
func steamLibraries(root string) []string {
	libs := []string{root}
	b, err := readLimited(filepath.Join(root, "steamapps/libraryfolders.vdf"), maxDesktopFile)
	if err != nil {
		return libs
	}
	v, err := parseVDF(string(b))
	if err != nil {
		return libs
	}
	for _, kv := range v.Get("libraryfolders").Kids {
		if p := kv.Node.Str("path"); filepath.IsAbs(p) {
			libs = append(libs, filepath.Clean(p))
		}
	}
	return uniqueDirs(libs)
}

type steamPlayed struct{ minutes, last int64 }

// steamPlaytime reads the playtime Steam records per app in the most
// recently written userdata/<account>/config/localconfig.vdf.
func steamPlaytime(root string) map[string]steamPlayed {
	out := map[string]steamPlayed{}
	accounts, err := os.ReadDir(filepath.Join(root, "userdata"))
	if err != nil {
		return out
	}
	var newest string
	var newestAt time.Time
	for _, a := range accounts {
		if _, err := strconv.ParseUint(a.Name(), 10, 64); err != nil {
			continue
		}
		p := filepath.Join(root, "userdata", a.Name(), "config/localconfig.vdf")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(newestAt) {
			newest, newestAt = p, fi.ModTime()
		}
	}
	if newest == "" {
		return out
	}
	b, err := readLimited(newest, maxVDFFile)
	if err != nil {
		return out
	}
	v, err := parseVDF(string(b))
	if err != nil {
		return out
	}
	for _, kv := range v.Get("UserLocalConfigStore", "Software", "Valve", "Steam", "apps").Kids {
		mins, _ := strconv.ParseInt(kv.Node.Str("Playtime"), 10, 64)
		last, _ := strconv.ParseInt(kv.Node.Str("LastPlayed"), 10, 64)
		out[kv.Key] = steamPlayed{minutes: max(mins, 0), last: max(last, 0)}
	}
	return out
}

// steamCover finds the library art Steam cached for appid: the portrait
// capsule first (library_capsule in newer clients), then the header. Newer clients keep it in a folder per
// app, sometimes one level further down; older ones name it <id>_<kind>.
func steamCover(root, appid string) string {
	cache := filepath.Join(root, "appcache/librarycache")
	for _, kind := range []string{"library_600x900", "library_capsule", "header", "library_header"} {
		for _, p := range []string{
			filepath.Join(cache, appid, kind+".jpg"),
			filepath.Join(cache, appid+"_"+kind+".jpg"),
		} {
			if regular(p) {
				return p
			}
		}
		subs, err := os.ReadDir(filepath.Join(cache, appid))
		if err != nil {
			continue
		}
		for _, s := range subs {
			if p := filepath.Join(cache, appid, s.Name(), kind+".jpg"); s.IsDir() && regular(p) {
				return p
			}
		}
	}
	return ""
}

func regular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
