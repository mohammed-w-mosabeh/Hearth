package discovery

// This file and games_*.go cover games (SPEC 7, ROADMAP 4.1): Steam,
// Lutris and Heroic libraries, and .desktop entries in the Game category.
// Everything is read from local files; covers are never fetched.

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Game sources.
const (
	GameSteam   = "steam"
	GameLutris  = "lutris"
	GameHeroic  = "heroic"
	GameDesktop = "desktop"
)

// Game is one installed game.
type Game struct {
	ID     string `json:"id"` // "<source>.<id in that source>"
	Name   string `json:"name"`
	Source string `json:"source"`
	Store  string `json:"store,omitempty"` // Heroic: epic or gog; Lutris: the runner
	Size   int64  `json:"size,omitempty"`  // bytes on disk, when the launcher records it
	// LastPlayed and Playtime come from the launcher's own records (Unix
	// seconds, seconds); Tracked is the time Hearth saw the game running.
	LastPlayed int64 `json:"last_played,omitempty"`
	Playtime   int64 `json:"playtime,omitempty"`
	Tracked    int64 `json:"tracked,omitempty"`
	HasCover   bool  `json:"has_cover"`
	// Launchable is false when the launcher the game needs is not installed.
	Launchable bool `json:"launchable"`

	cover string // file, from paths the scan resolved itself
	appID string // desktop app the game starts through
	url   string // passed to that app for %u/%U; "" for desktop games
}

// gameIDRe matches the IDs the scan produces.
var gameIDRe = regexp.MustCompile(`^(steam\.[0-9]{1,10}|lutris\.[0-9]{1,10}|heroic\.(legendary|gog)\.[A-Za-z0-9_-]{1,100}|desktop\.[A-Za-z0-9_][A-Za-z0-9._+~@-]{0,199})$`)

// ValidGameID reports whether s can be a game ID.
func ValidGameID(s string) bool { return gameIDRe.MatchString(s) }

// Playtimes is the store of the time Hearth tracked (store.DB).
type Playtimes interface {
	Playtimes(ctx context.Context) (map[string]int64, error)
	AddPlaytime(ctx context.Context, id string, seconds int64, at time.Time) error
}

// GamesConfig configures the game list.
type GamesConfig struct {
	Home string
	Apps *Index
	// Running lists app IDs from unit names (collect.RunningApps).
	Running func() []string
	Store   Playtimes
	Log     *slog.Logger
	// Proc and UID are replaced in tests (Playing).
	Proc string
	UID  int
}

// Games is the game list, scanned on demand.
type Games struct {
	cfg GamesConfig

	mu      sync.Mutex
	games   []Game
	byID    map[string]int
	scanned time.Time
}

// gamesMaxAge is how long a scan is reused; the page reloads on demand.
const gamesMaxAge = 15 * time.Second

// NewGames returns a Games.
func NewGames(cfg GamesConfig) *Games {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Games{cfg: cfg, byID: map[string]int{}}
}

// Launchers: the desktop IDs each library's games start through, in order
// of preference (Debian package, Flatpak).
var launcherApps = map[string][]string{
	GameSteam:  {"steam", "com.valvesoftware.Steam"},
	GameLutris: {"net.lutris.Lutris", "lutris"},
	GameHeroic: {"com.heroicgameslauncher.hgl", "heroic"},
}

// launcherURL marks .desktop shortcuts the launchers create for their own
// games; the library entry already lists those.
var launcherURL = []string{"steam://rungameid/", "lutris:rungameid/", "heroic://launch"}

// List returns the games, rescanning if the last scan is old.
func (g *Games) List() []Game {
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.scanned) > gamesMaxAge {
		g.scanLocked()
	}
	return g.games
}

// Game returns the game with id from the last scan, rescanning if old.
func (g *Games) Game(id string) (Game, bool) {
	g.List()
	g.mu.Lock()
	defer g.mu.Unlock()
	i, ok := g.byID[id]
	if !ok {
		return Game{}, false
	}
	return g.games[i], true
}

func (g *Games) scanLocked() {
	var games []Game
	games = append(games, steamGames(g.cfg.Home)...)
	games = append(games, lutrisGames(g.cfg.Home)...)
	games = append(games, heroicGames(g.cfg.Home)...)
	games = append(games, g.desktopGames()...)

	var tracked map[string]int64
	if g.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		if tracked, err = g.cfg.Store.Playtimes(ctx); err != nil {
			g.cfg.Log.Warn("games: cannot read tracked playtime", "err", err)
		}
		cancel()
	}
	seen := map[string]bool{}
	out := games[:0]
	for _, gm := range games {
		if seen[gm.ID] || !ValidGameID(gm.ID) || gm.Name == "" {
			continue
		}
		seen[gm.ID] = true
		gm.Tracked = tracked[gm.ID]
		gm.HasCover = gm.cover != ""
		if gm.Source != GameDesktop {
			gm.appID = g.launcher(gm.Source)
		}
		gm.Launchable = gm.appID != ""
		out = append(out, gm)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	byID := make(map[string]int, len(out))
	for i, gm := range out {
		byID[gm.ID] = i
	}
	g.games, g.byID, g.scanned = out, byID, time.Now()
}

// launcher is the installed app that starts games of source, or "".
func (g *Games) launcher(source string) string {
	if g.cfg.Apps == nil {
		return ""
	}
	for _, id := range launcherApps[source] {
		if _, ok := g.cfg.Apps.App(id); ok {
			return id
		}
	}
	return ""
}

// desktopGames are the apps in the Game category, without the launchers
// themselves and the shortcuts they create.
func (g *Games) desktopGames() []Game {
	if g.cfg.Apps == nil {
		return nil
	}
	isLauncher := map[string]bool{}
	for _, ids := range launcherApps {
		for _, id := range ids {
			isLauncher[strings.ToLower(id)] = true
		}
	}
	var out []Game
	for _, a := range g.cfg.Apps.Apps() {
		if !hasFold(a.Categories, "Game") || isLauncher[strings.ToLower(a.ID)] || isLauncher[strings.ToLower(a.FlatpakID)] {
			continue
		}
		if runsLauncher(a.Exec) {
			continue
		}
		out = append(out, Game{ID: GameDesktop + "." + a.ID, Name: a.Name, Source: GameDesktop,
			cover: a.iconPath, appID: a.ID})
	}
	return out
}

// launcherProgs are the launchers' programs and Flatpak IDs as Exec lines
// name them; an entry that runs one is a launcher (a custom Steam entry)
// or a shortcut to a game its library already lists.
var launcherProgs = map[string]bool{"steam": true, "lutris": true, "heroic": true,
	"com.valvesoftware.steam": true, "net.lutris.lutris": true, "com.heroicgameslauncher.hgl": true}

func runsLauncher(exec string) bool {
	for _, u := range launcherURL {
		if strings.Contains(exec, u) {
			return true
		}
	}
	for i, w := range strings.Fields(exec) {
		w = strings.ToLower(strings.Trim(w, `"'`))
		if (i == 0 && launcherProgs[filepath.Base(w)]) || (i > 0 && launcherProgs[w]) {
			return true
		}
	}
	return false
}

func hasFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// LaunchTarget says how to start game id: through the desktop app appID,
// with url for its %u/%U field code ("" for none).
func (g *Games) LaunchTarget(id string) (name, appID, url string, ok bool) {
	gm, found := g.Game(id)
	if !found || gm.appID == "" {
		return gm.Name, "", "", found
	}
	return gm.Name, gm.appID, gm.url, true
}

// Track adds the time desktop games run to the store, sampling the
// running apps every interval until ctx ends. Games started from a
// launcher run inside the launcher's unit, so the launcher's own playtime
// is the one shown for them.
func (g *Games) Track(ctx context.Context, interval time.Duration) {
	if g.cfg.Store == nil || g.cfg.Running == nil || g.cfg.Apps == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			desktop := map[string]bool{}
			for _, gm := range g.List() {
				if gm.Source == GameDesktop {
					desktop[gm.appID] = true
				}
			}
			if len(desktop) == 0 {
				continue
			}
			for appID := range g.cfg.Apps.Running(g.cfg.Running()) {
				if !desktop[appID] {
					continue
				}
				if err := g.cfg.Store.AddPlaytime(ctx, GameDesktop+"."+appID, int64(interval/time.Second), now); err != nil {
					g.cfg.Log.Warn("games: cannot record playtime", "err", err)
				}
			}
		}
	}
}

// maxCover bounds a cover sent to the UI (Steam's 600x900 art is ~100 KiB).
const maxCover = 1 << 20

// CoverData returns the game's cover as a data: URI, or "".
func (g *Games) CoverData(id string) string {
	gm, ok := g.Game(id)
	if !ok || gm.cover == "" {
		return ""
	}
	b, err := readLimited(gm.cover, maxCover)
	if err != nil || len(b) == 0 {
		return ""
	}
	var mime string
	switch {
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		mime = "image/jpeg"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		mime = "image/png"
	case len(b) > 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP":
		mime = "image/webp"
	case strings.EqualFold(filepath.Ext(gm.cover), ".svg"):
		mime = "image/svg+xml"
	default:
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// maxCoverIDs bounds one cover batch (covers are bigger than icons).
const maxCoverIDs = 24

// HandleList serves GET /api/v1/games.
func (g *Games) HandleList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"games": g.List()})
}

// HandleCovers serves GET /api/v1/games/covers?id=a&id=b as {"a": "data:..."}.
func (g *Games) HandleCovers(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 || len(ids) > maxCoverIDs {
		http.Error(w, "1 to 24 id= parameters required", http.StatusBadRequest)
		return
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if !ValidGameID(id) {
			continue
		}
		if d := g.CoverData(id); d != "" {
			out[id] = d
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, out)
}
