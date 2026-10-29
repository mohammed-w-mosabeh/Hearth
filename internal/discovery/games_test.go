package discovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseVDF(t *testing.T) {
	v, err := parseVDF(`// comment
"libraryfolders"
{
	"0"
	{
		"path"		"/home/u/.local/share/Steam"
		"apps" { "220" "123" }
	}
	"1" { "path" "/media/d/Steam \"Lib\"" [$LINUX] }
	bare value
}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Str("LibraryFolders", "0", "path"); got != "/home/u/.local/share/Steam" {
		t.Errorf("path %q", got)
	}
	if got := v.Str("libraryfolders", "1", "path"); got != `/media/d/Steam "Lib"` {
		t.Errorf("escaped %q", got)
	}
	if got := v.Str("libraryfolders", "bare"); got != "value" {
		t.Errorf("unquoted %q", got)
	}
	if v.Get("libraryfolders", "0", "apps", "220").Value != "123" || v.Get("nope", "x") != nil {
		t.Error("Get")
	}
	for _, bad := range []string{`"a" {`, `"a" }`, `{`, `"a" "b`, `"a"`, `"a" { "b" { "c" "d" }`, strings.Repeat(`"a" {`, 40) + strings.Repeat("}", 40)} {
		if _, err := parseVDF(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func FuzzParseVDF(f *testing.F) {
	f.Add(`"AppState" { "appid" "220" "name" "Half-Life 2" }`)
	f.Add(`"a" { "b" { [$X] "c" "d\"" } } // x`)
	f.Fuzz(func(t *testing.T, s string) {
		v, err := parseVDF(s)
		if err == nil && v == nil {
			t.Fatal("nil root without error")
		}
	})
}

func acf(appid, name, flags, size, last string) string {
	return `"AppState"
{
	"appid"		"` + appid + `"
	"name"		"` + name + `"
	"StateFlags"		"` + flags + `"
	"SizeOnDisk"		"` + size + `"
	"LastPlayed"		"` + last + `"
}`
}

type playStore struct {
	mu    sync.Mutex
	secs  map[string]int64
	added []string
}

func (s *playStore) Playtimes(context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for k, v := range s.secs {
		out[k] = v
	}
	return out, nil
}

func (s *playStore) AddPlaytime(_ context.Context, id string, secs int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secs[id] += secs
	s.added = append(s.added, id)
	return nil
}

// gamesTree builds a home with every kind of library.
func gamesTree(t *testing.T) (tree, *Index, string) {
	tr, cfg := newTree(t)
	home := cfg.Home
	apps := "home/.local/share/applications/"
	tr.file(apps+"com.valvesoftware.Steam.desktop", desktop("Steam", "Categories=Game;", "X-Flatpak=com.valvesoftware.Steam",
		"Exec=flatpak run com.valvesoftware.Steam @@u %U @@"), 0o600)
	tr.file(apps+"supertux2.desktop", desktop("SuperTux", "Categories=Game;ArcadeGame;", "Icon=supertux"), 0o600)
	tr.file(apps+"steam_app_220.desktop", desktop("Half-Life 2 shortcut", "Categories=Game;", "Exec=steam steam://rungameid/220"), 0o600)
	tr.file(apps+"steam-custom.desktop", desktop("Steam (no sandbox)", "Categories=Game;", "Exec=flatpak run com.valvesoftware.Steam -no-cef-sandbox"), 0o600)
	tr.file(apps+"lutris-x.desktop", desktop("Lutris", "Categories=Game;", "Exec=/usr/bin/lutris"), 0o600)
	tr.file("usr/share/icons/hicolor/128x128/apps/supertux.png", "\x89PNG\r\n\x1a\nfake", 0o600)

	// Flatpak Steam with a second library elsewhere.
	steam := ".var/app/com.valvesoftware.Steam/.local/share/Steam"
	lib2 := filepath.Join(tr.root, "media/disk/SteamLibrary")
	tr.file("home/"+steam+"/steamapps/libraryfolders.vdf", `"libraryfolders" { "0" { "path" "`+filepath.Join(home, steam)+`" }
		"1" { "path" "`+lib2+`" } "2" { "path" "relative/ignored" } }`, 0o600)
	tr.file("home/"+steam+"/steamapps/appmanifest_220.acf", acf("220", "Half-Life 2", "4", "6500000000", "1700000000"), 0o600)
	tr.file("home/"+steam+"/steamapps/appmanifest_1070560.acf", acf("1070560", "Steam Linux Runtime 1.0 (scout)", "4", "1", "0"), 0o600)
	tr.file("home/"+steam+"/steamapps/appmanifest_1493710.acf", acf("1493710", "Proton Experimental", "4", "1", "0"), 0o600)
	tr.file("home/"+steam+"/steamapps/appmanifest_400.acf", acf("400", "Portal", "1026", "1", "0"), 0o600) // updating: bit 4 not set
	tr.file("media/disk/SteamLibrary/steamapps/appmanifest_70.acf", acf("70", "Half-Life", "4", "500", "0"), 0o600)
	tr.file("media/disk/SteamLibrary/steamapps/appmanifest_bad.acf", acf("x", "Bad", "4", "1", "0"), 0o600)
	tr.file("home/"+steam+"/userdata/42/config/localconfig.vdf", `"UserLocalConfigStore" { "Software" { "Valve" { "Steam" { "apps" {
		"220" { "LastPlayed" "1800000000" "Playtime" "90" } } } } } }`, 0o600)
	tr.file("home/"+steam+"/appcache/librarycache/220/library_600x900.jpg", "\xff\xd8\xff cover", 0o600)
	tr.file("home/"+steam+"/appcache/librarycache/80/def/library_capsule.jpg", "\xff\xd8\xff capsule", 0o600)
	tr.file("home/"+steam+"/steamapps/appmanifest_80.acf", acf("80", "Condition Zero", "4", "1", "0"), 0o600)
	tr.file("home/"+steam+"/appcache/librarycache/70/abc/header.jpg", "\xff\xd8\xff header", 0o600)

	// Heroic: one Epic and one GOG game, a GOG DLC.
	h := "home/.config/heroic/"
	tr.file(h+"legendaryConfig/legendary/installed.json", `{"Fortnite": {"app_name": "Fortnite", "title": "Fortnite", "install_path": "/g/Fortnite", "install_size": 1000},
		"../evil": {"app_name": "../evil", "title": "Evil"}}`, 0o600)
	tr.file(h+"gog_store/installed.json", `{"installed": [{"appName": "1207658924", "install_path": "/g/Witcher", "install_size": "1.5 GiB"},
		{"appName": "99", "install_path": "/g/DLC", "is_dlc": true}]}`, 0o600)
	tr.file(h+"gog_store/library.json", `{"games": [{"app_name": "1207658924", "title": "The Witcher", "art_square": "https://img/w.jpg"}]}`, 0o600)
	sum := sha256.Sum256([]byte("https://img/w.jpg"))
	tr.file(h+"images-cache/"+hex.EncodeToString(sum[:]), "RIFF\x00\x00\x00\x00WEBPVP8 ", 0o600)
	tr.file(h+"store/timestamp.json", `{"1207658924": {"lastPlayed": "2026-01-02T03:04:05.000Z", "totalPlayed": 30}}`, 0o600)

	// Lutris.
	lutrisDB(t, filepath.Join(home, ".local/share/lutris/pga.db"))
	tr.file("home/.cache/lutris/coverart/osu.jpg", "\xff\xd8\xff osu", 0o600)

	x := NewIndex(cfg)
	x.Scan()
	return tr, x, home
}

func lutrisDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{
		`CREATE TABLE games (id INTEGER PRIMARY KEY, name TEXT, slug TEXT, runner TEXT, playtime REAL, lastplayed INTEGER, installed INTEGER, hidden INTEGER)`,
		`INSERT INTO games VALUES (3, 'osu!', 'osu', 'wine', 1.5, 1750000000, 1, 0), (4, 'Removed', 'removed', 'wine', 0, 0, 0, 0),
			(5, 'Hidden', 'hidden', 'linux', 0, 0, 1, 1), (6, 'Odd slug', '../x', 'linux', NULL, NULL, 1, 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGames(t *testing.T) {
	_, x, home := gamesTree(t)
	st := &playStore{secs: map[string]int64{"desktop.supertux2": 120}}
	g := NewGames(GamesConfig{Home: home, Apps: x, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	byID := map[string]Game{}
	var names []string
	for _, gm := range g.List() {
		byID[gm.ID] = gm
		names = append(names, gm.Name)
	}
	want := "Condition Zero,Fortnite,Half-Life,Half-Life 2,Odd slug,osu!,SuperTux,The Witcher"
	if strings.Join(names, ",") != want {
		t.Fatalf("games %s, want %s", strings.Join(names, ","), want)
	}
	hl2 := byID["steam.220"]
	if hl2.Size != 6500000000 || hl2.Playtime != 5400 || hl2.LastPlayed != 1800000000 || !hl2.HasCover || !hl2.Launchable ||
		hl2.appID != "com.valvesoftware.Steam" || hl2.url != "steam://rungameid/220" {
		t.Errorf("hl2 %+v", hl2)
	}
	if hl := byID["steam.70"]; !strings.HasSuffix(hl.cover, "70/abc/header.jpg") {
		t.Errorf("header fallback %q", hl.cover)
	}
	if cz := byID["steam.80"]; !strings.HasSuffix(cz.cover, "80/def/library_capsule.jpg") {
		t.Errorf("capsule %q", cz.cover)
	}
	osu := byID["lutris.3"]
	if osu.Playtime != 5400 || osu.LastPlayed != 1750000000 || osu.Store != "wine" || !osu.HasCover || osu.Launchable || osu.url != "lutris:rungameid/3" {
		t.Errorf("osu %+v", osu)
	}
	if byID["lutris.6"].HasCover {
		t.Error("cover looked up for an unsafe slug")
	}
	w := byID["heroic.gog.1207658924"]
	if w.Store != "gog" || w.Size != 1610612736 || w.Playtime != 1800 || w.LastPlayed != 1767323045 || !w.HasCover || w.url != "heroic://launch/gog/1207658924" {
		t.Errorf("witcher %+v", w)
	}
	if fn := byID["heroic.legendary.Fortnite"]; fn.Store != "epic" || fn.Size != 1000 || fn.HasCover {
		t.Errorf("fortnite %+v", fn)
	}
	st2 := byID["desktop.supertux2"]
	if st2.Tracked != 120 || !st2.HasCover || !st2.Launchable || st2.appID != "supertux2" {
		t.Errorf("supertux %+v", st2)
	}

	if d := g.CoverData("steam.220"); !strings.HasPrefix(d, "data:image/jpeg;base64,") {
		t.Errorf("jpeg cover %.40q", d)
	}
	if d := g.CoverData("heroic.gog.1207658924"); !strings.HasPrefix(d, "data:image/webp;base64,") {
		t.Errorf("webp cover %.40q", d)
	}
	if d := g.CoverData("desktop.supertux2"); !strings.HasPrefix(d, "data:image/png;base64,") {
		t.Errorf("icon cover %.40q", d)
	}
	if name, app, u, ok := g.LaunchTarget("steam.220"); !ok || name != "Half-Life 2" || app != "com.valvesoftware.Steam" || u != "steam://rungameid/220" {
		t.Errorf("target %s %s %s %v", name, app, u, ok)
	}
	if _, app, _, ok := g.LaunchTarget("lutris.3"); !ok || app != "" {
		t.Errorf("lutris without Lutris: %q %v", app, ok)
	}
	if _, _, _, ok := g.LaunchTarget("steam.1"); ok {
		t.Error("unknown game")
	}
}

func TestGamesTrack(t *testing.T) {
	_, x, home := gamesTree(t)
	st := &playStore{secs: map[string]int64{}}
	g := NewGames(GamesConfig{Home: home, Apps: x, Store: st, Running: func() []string { return []string{"supertux2", "firefox"} },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go g.Track(ctx, 20*time.Millisecond)
	for {
		st.mu.Lock()
		n := len(st.added)
		st.mu.Unlock()
		if n > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("no playtime recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, id := range st.added {
		if id != "desktop.supertux2" {
			t.Errorf("tracked %s", id)
		}
	}
}

func TestValidGameID(t *testing.T) {
	for _, ok := range []string{"steam.220", "lutris.3", "heroic.gog.1207658924", "heroic.legendary.Fortnite", "desktop.org.supertux.SuperTux"} {
		if !ValidGameID(ok) {
			t.Errorf("refused %s", ok)
		}
	}
	for _, bad := range []string{"", "steam.", "steam.x", "steam.12345678901", "heroic.epic.x", "heroic.gog.../x", "desktop.", "desktop./x", "other.1", "steam.1\n"} {
		if ValidGameID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func FuzzValidGameID(f *testing.F) {
	f.Add("steam.220")
	f.Add("heroic.gog.x")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidGameID(s) && (strings.ContainsAny(s, "/\\ \n\x00") || strings.Contains(s, "..") && !strings.HasPrefix(s, "desktop.")) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func TestParseSize(t *testing.T) {
	for s, want := range map[string]int64{"1.5 GiB": 1610612736, "830 MB": 830000000, "12": 12, "x GiB": 0, "-1 GB": 0, "1 PB": 0} {
		if got := parseSize(s); got != want {
			t.Errorf("%q = %d, want %d", s, got, want)
		}
	}
}

func TestHandleCovers(t *testing.T) {
	_, x, home := gamesTree(t)
	g := NewGames(GamesConfig{Home: home, Apps: x})
	get := func(q string) (int, map[string]string) {
		w := httptest.NewRecorder()
		g.HandleCovers(w, httptest.NewRequest(http.MethodGet, "/api/v1/games/covers?"+q, nil))
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := get("id=steam.220&id=../etc&id=steam.1"); code != 200 || len(out) != 1 || out["steam.220"] == "" {
		t.Errorf("%d %v", code, out)
	}
	if code, _ := get(""); code != http.StatusBadRequest {
		t.Errorf("no ids: %d", code)
	}
	if code, _ := get(strings.Repeat("id=steam.1&", 25)); code != http.StatusBadRequest {
		t.Errorf("too many: %d", code)
	}
}
