package discovery

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // the "sqlite" driver, for Lutris's pga.db
)

// lutrisDirs are Lutris's data and cache directories: the Debian package,
// then Flatpak. Older versions keep covers in the cache directory.
func lutrisDirs(home string) (data, cache []string) {
	return uniqueDirs([]string{
			filepath.Join(home, ".local/share/lutris"),
			filepath.Join(home, ".var/app/net.lutris.Lutris/data/lutris"),
		}), uniqueDirs([]string{
			filepath.Join(home, ".cache/lutris"),
			filepath.Join(home, ".var/app/net.lutris.Lutris/cache/lutris"),
		})
}

// lutrisSlug is what a slug may be before it is used in a file name.
var lutrisSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

func lutrisGames(home string) []Game {
	data, cache := lutrisDirs(home)
	var out []Game
	for _, d := range data {
		rows, err := lutrisRows(filepath.Join(d, "pga.db"))
		if err != nil {
			continue
		}
		for _, r := range rows {
			gm := Game{ID: "lutris." + strconv.FormatInt(r.id, 10), Name: r.name, Source: GameLutris, Store: r.runner,
				LastPlayed: max(r.lastPlayed, 0), Playtime: int64(max(r.hours, 0) * 3600),
				url: "lutris:rungameid/" + strconv.FormatInt(r.id, 10), dir: r.dir}
			if lutrisSlug.MatchString(r.slug) {
				gm.cover = lutrisCover(append([]string{d}, cache...), r.slug)
			}
			out = append(out, gm)
		}
	}
	return out
}

type lutrisRow struct {
	id                 int64
	name, slug, runner string
	dir                string // the game's folder, as Lutris records it
	hours              float64
	lastPlayed         int64
}

// lutrisRows reads the installed games. hearthd's home is read-only, so
// the database is opened read-only, and as immutable if SQLite cannot
// open it that way (a WAL database needs its -shm file writable).
func lutrisRows(path string) ([]lutrisRow, error) {
	if !regular(path) {
		return nil, sql.ErrNoRows
	}
	var rows []lutrisRow
	var err error
	for _, mode := range []string{"mode=ro", "mode=ro&immutable=1"} {
		if rows, err = lutrisQuery(path, mode); err == nil {
			return rows, nil
		}
	}
	return nil, err
}

// lutrisQueries read the installed games, newest schema first: hidden and
// directory are newer than some databases.
var lutrisQueries = []string{
	`SELECT id, COALESCE(name, ''), COALESCE(slug, ''), COALESCE(runner, ''), COALESCE(CAST(playtime AS REAL), 0),
		COALESCE(CAST(lastplayed AS INTEGER), 0), COALESCE(directory, '') FROM games WHERE installed = 1 AND COALESCE(hidden, 0) = 0 LIMIT 10000`,
	`SELECT id, COALESCE(name, ''), COALESCE(slug, ''), COALESCE(runner, ''), COALESCE(CAST(playtime AS REAL), 0),
		COALESCE(CAST(lastplayed AS INTEGER), 0), COALESCE(directory, '') FROM games WHERE installed = 1 LIMIT 10000`,
	`SELECT id, COALESCE(name, ''), COALESCE(slug, ''), COALESCE(runner, ''), COALESCE(CAST(playtime AS REAL), 0),
		COALESCE(CAST(lastplayed AS INTEGER), 0), '' FROM games WHERE installed = 1 AND COALESCE(hidden, 0) = 0 LIMIT 10000`,
	`SELECT id, COALESCE(name, ''), COALESCE(slug, ''), COALESCE(runner, ''), COALESCE(CAST(playtime AS REAL), 0),
		COALESCE(CAST(lastplayed AS INTEGER), 0), '' FROM games WHERE installed = 1 LIMIT 10000`,
}

func lutrisQuery(path, mode string) ([]lutrisRow, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: mode + "&_pragma=busy_timeout(1000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Columns vary across Lutris versions in type more than in name;
	// CAST and COALESCE keep the scan simple.
	var res *sql.Rows
	for _, q := range lutrisQueries {
		if res, err = db.QueryContext(ctx, q); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Close() }()
	var out []lutrisRow
	for res.Next() {
		var r lutrisRow
		if err := res.Scan(&r.id, &r.name, &r.slug, &r.runner, &r.hours, &r.lastPlayed, &r.dir); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, res.Err()
}

// lutrisCover finds the cover art, then the banner, for slug.
func lutrisCover(dirs []string, slug string) string {
	for _, kind := range []string{"coverart", "banners"} {
		for _, d := range dirs {
			for _, ext := range []string{".jpg", ".png"} {
				if p := filepath.Join(d, kind, slug+ext); regular(p) {
					return p
				}
			}
		}
	}
	return ""
}
