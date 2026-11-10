package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxHeroicFile bounds a Heroic JSON store; a large library is a few MiB.
const maxHeroicFile = 32 << 20

// heroicDirs are Heroic's config directories: the Debian package, Flatpak.
func heroicDirs(home string) []string {
	return uniqueDirs([]string{
		filepath.Join(home, ".config/heroic"),
		filepath.Join(home, ".var/app/com.heroicgameslauncher.hgl/config/heroic"),
	})
}

// heroicApp is what an Epic or GOG app name may be in a game ID and URL.
var heroicApp = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// heroicStores: Heroic's runner name and the store it stands for.
var heroicStores = map[string]string{"legendary": "epic", "gog": "gog"}

func heroicGames(home string) []Game {
	var out []Game
	for _, d := range heroicDirs(home) {
		played := heroicPlaytime(d)
		art := heroicArt(d)
		add := func(runner, app, title, installPath string, size int64) {
			if !heroicApp.MatchString(app) {
				return
			}
			if title == "" {
				title = art[runner+"."+app].title
			}
			if title == "" && installPath != "" {
				title = filepath.Base(installPath)
			}
			gm := Game{ID: "heroic." + runner + "." + app, Name: title, Source: GameHeroic, Store: heroicStores[runner],
				Size: max(size, 0), url: "heroic://launch/" + runner + "/" + app, dir: installPath}
			if p, ok := played[app]; ok {
				gm.Playtime, gm.LastPlayed = p.minutes*60, p.last
			}
			if u := art[runner+"."+app].url; u != "" {
				gm.cover = heroicCachedImage(d, u)
			}
			out = append(out, gm)
		}

		// Epic, through legendary: {"<app>": {app_name, title, install_path, install_size}}.
		var epic map[string]struct {
			AppName     string `json:"app_name"`
			Title       string `json:"title"`
			InstallPath string `json:"install_path"`
			InstallSize int64  `json:"install_size"`
		}
		if readJSON(filepath.Join(d, "legendaryConfig/legendary/installed.json"), &epic) {
			for k, v := range epic {
				app := v.AppName
				if app == "" {
					app = k
				}
				add("legendary", app, v.Title, v.InstallPath, v.InstallSize)
			}
		}
		// GOG: {"installed": [{appName, install_path, install_size: "1.2 GiB"}]}.
		var gog struct {
			Installed []struct {
				AppName     string `json:"appName"`
				InstallPath string `json:"install_path"`
				InstallSize string `json:"install_size"`
				IsDLC       bool   `json:"is_dlc"`
			} `json:"installed"`
		}
		if readJSON(filepath.Join(d, "gog_store/installed.json"), &gog) {
			for _, v := range gog.Installed {
				if !v.IsDLC {
					add("gog", v.AppName, "", v.InstallPath, parseSize(v.InstallSize))
				}
			}
		}
	}
	return out
}

func readJSON(p string, v any) bool {
	b, err := readLimited(p, maxHeroicFile)
	return err == nil && json.Unmarshal(b, v) == nil
}

type heroicPlayed struct{ minutes, last int64 }

// heroicPlaytime reads store/timestamp.json:
// {"<app>": {"lastPlayed": RFC 3339, "totalPlayed": minutes}}.
func heroicPlaytime(dir string) map[string]heroicPlayed {
	var raw map[string]struct {
		LastPlayed  string  `json:"lastPlayed"`
		TotalPlayed float64 `json:"totalPlayed"`
	}
	out := map[string]heroicPlayed{}
	if !readJSON(filepath.Join(dir, "store/timestamp.json"), &raw) {
		return out
	}
	for app, v := range raw {
		var last int64
		if t, err := time.Parse(time.RFC3339, v.LastPlayed); err == nil {
			last = t.Unix()
		}
		out[app] = heroicPlayed{minutes: int64(max(v.TotalPlayed, 0)), last: max(last, 0)}
	}
	return out
}

type heroicEntry struct{ title, url string }

// heroicArt maps "<runner>.<app>" to its title and cover URL from the
// library caches. The URL only names the file Heroic cached; nothing is
// fetched.
func heroicArt(dir string) map[string]heroicEntry {
	type item struct {
		AppName   string `json:"app_name"`
		Title     string `json:"title"`
		ArtSquare string `json:"art_square"`
	}
	out := map[string]heroicEntry{}
	addAll := func(runner string, items []item) {
		for _, it := range items {
			if it.AppName != "" {
				out[runner+"."+it.AppName] = heroicEntry{title: it.Title, url: it.ArtSquare}
			}
		}
	}
	var epic struct {
		Library []item `json:"library"`
	}
	if readJSON(filepath.Join(dir, "store_cache/legendary_library.json"), &epic) {
		addAll("legendary", epic.Library)
	}
	for _, p := range []string{"store_cache/gog_library.json", "gog_store/library.json"} {
		var gog struct {
			Games []item `json:"games"`
		}
		if readJSON(filepath.Join(dir, p), &gog) {
			addAll("gog", gog.Games)
		}
	}
	return out
}

// heroicCachedImage is Heroic's cached copy of url (images-cache/<sha256
// of the URL>), or "".
func heroicCachedImage(dir, url string) string {
	sum := sha256.Sum256([]byte(url))
	if p := filepath.Join(dir, "images-cache", hex.EncodeToString(sum[:])); regular(p) {
		return p
	}
	return ""
}

// parseSize reads sizes like "1.22 GiB" or "830 MB"; 0 if it cannot.
func parseSize(s string) int64 {
	num, unit, _ := strings.Cut(strings.TrimSpace(s), " ")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0
	}
	mult := map[string]float64{"": 1, "B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40}[strings.ToUpper(unit)]
	return int64(f * mult)
}
