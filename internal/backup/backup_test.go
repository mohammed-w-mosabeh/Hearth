package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/profiles"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type toggle struct{ on bool }

func (t *toggle) Enabled(context.Context) (bool, error) { return t.on, nil }
func (t *toggle) SetEnabled(_ context.Context, on bool) error {
	t.on = on
	return nil
}

func fixture(t *testing.T) (*Manager, *store.DB, *time.Time) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	m := New(Config{DB: db, Dir: filepath.Join(t.TempDir(), "backups"), Weekly: &toggle{},
		ValidateProfile: func(name string, _ []profiles.Item) error {
			if name == "bad" {
				return errors.New("refused")
			}
			return nil
		},
		Clock: func() time.Time { return now }})
	return m, db, &now
}

func TestCopies(t *testing.T) {
	m, db, now := fixture(t)
	ctx := context.Background()
	if err := db.SetSetting(ctx, "marker", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	var last Copy
	for range 6 {
		*now = now.Add(time.Hour)
		c, err := m.Create(ctx)
		if err != nil {
			t.Fatal(err)
		}
		last = c
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != keep || list[0].Name != last.Name {
		t.Fatalf("%+v", list)
	}
	fi, err := os.Stat(m.cfg.Dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatal(fi, err)
	}
	path := filepath.Join(m.cfg.Dir, last.Name)
	if fi, err = os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	entries, err := os.ReadDir(m.cfg.Dir)
	if err != nil || len(entries) != keep {
		t.Fatal(entries, err)
	}
	copied, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copied.Close() }()
	var v string
	if err := copied.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil || v != "1" {
		t.Fatal(v, err)
	}
}

func TestOnlyItsOwnFilesAreRemoved(t *testing.T) {
	m, _, now := fixture(t)
	if err := os.MkdirAll(m.cfg.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(m.cfg.Dir, "mine.db")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range keep + 2 {
		*now = now.Add(time.Hour)
		if _, err := m.Create(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
}

func TestDue(t *testing.T) {
	m, _, now := fixture(t)
	if due, err := m.due(); err != nil || !due {
		t.Fatal(due, err)
	}
	if _, err := m.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	*now = real.Add(6 * 24 * time.Hour)
	if due, _ := m.due(); due {
		t.Fatal("due after six days")
	}
	*now = real.Add(8 * 24 * time.Hour)
	if due, _ := m.due(); !due {
		t.Fatal("not due after eight days")
	}
}

func TestCleanURL(t *testing.T) {
	for in, want := range map[string]string{ // #nosec G101 -- fake credentials, the input under test
		"http://a.lan:8080/x?y=1":            "http://a.lan:8080/x?y=1",
		"http://a.lan/?token=abc&view=2":     "http://a.lan/?view=2",
		"http://a.lan/?APIKEY=abc":           "http://a.lan/",
		"http://a.lan/#access_token=abc":     "http://a.lan/",
		"http://a.lan/?password=a&Auth=b#ok": "http://a.lan/#ok",
		"http://a.lan/?a=1;token=abc":        "http://a.lan/?a=1",
		"http://a.lan/?%74oken=abc&z=1&b=2":  "http://a.lan/?z=1&b=2",
	} {
		if got := cleanURL(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func seed(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.SetSetting(ctx, "apps.favorites", json.RawMessage(`{"ids":["org.gnome.TextEditor"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "security.cves", json.RawMessage(`{"state":"not a preference"}`)); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	if err := db.PutIcon(ctx, sha, "image/png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTile(ctx, store.Tile{Name: "Grafana", URL: "http://g.lan/?token=hunter2&view=2", Icon: "upload:" + sha, HealthCheck: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddProfile(ctx, store.Profile{Name: "Work", Items: `[{"kind":"unit","target":"user/a.service","do":"start"}]`, AutoGame: true}); err != nil {
		t.Fatal(err)
	}
}

func dump(t *testing.T, f File) string {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func importReq(s string) actions.Request {
	return actions.Request{Target: "settings", Params: map[string]string{"file": s}}
}

func TestExportHoldsNoSecretOrState(t *testing.T) {
	m, db, _ := fixture(t)
	seed(t, db)
	f, err := m.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := dump(t, f)
	for _, bad := range []string{"hunter2", "upload:", "auto_game", "security.cves", "not a preference", `"id"`} {
		if strings.Contains(s, bad) {
			t.Errorf("export holds %q: %s", bad, s)
		}
	}
	for _, good := range []string{"http://g.lan/?view=2", "org.gnome.TextEditor", "Disk almost full", "user/a.service"} {
		if !strings.Contains(s, good) {
			t.Errorf("export lacks %q", good)
		}
	}
}

// An export read back changes nothing: not the token that stayed out of it,
// and not the uploaded icon.
func TestExportImportIsANoOp(t *testing.T) {
	m, db, _ := fixture(t)
	seed(t, db)
	ctx := context.Background()
	f, err := m.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.plan(ctx, importReq(dump(t, f)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 0 || len(p.Blocked) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestImportAppliesWhatWasPreviewed(t *testing.T) {
	m, db, _ := fixture(t)
	seed(t, db)
	ctx := context.Background()
	f, err := m.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.WeeklyReport = true
	f.Settings["home.layout"] = json.RawMessage(`{ "order": ["cpu"], "hidden": [] }`)
	f.AlertRules[0].Threshold = 80
	f.AlertRules = append(f.AlertRules, Rule{Name: "New", Metric: "cpu.usage", Op: ">", Threshold: 50, Severity: "info", Enabled: true})
	f.Tiles[0].Description = "graphs"
	f.Tiles = append(f.Tiles, Tile{Name: "Added", URL: "https://b.lan/"})
	f.Profiles[0].Items = []profiles.Item{{Kind: "app", Target: "calc", Do: "start"}}
	f.Profiles = append(f.Profiles, Profile{Name: "Play", Items: []profiles.Item{{Kind: "app", Target: "game", Do: "start"}}})

	p, err := m.plan(ctx, importReq(dump(t, f)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 8 || len(p.Blocked) != 0 {
		t.Fatalf("%+v", p)
	}
	for _, it := range p.Items {
		if it.ID == "tile:Added" && it.Name != "Added (https://b.lan/)" {
			t.Errorf("the preview hides where a tile leads: %+v", it)
		}
		if strings.Contains(it.Name, "hunter2") {
			t.Errorf("the preview shows a credential: %+v", it)
		}
	}
	res, err := m.run(ctx, actions.Request{}, p)
	if err != nil || res.Done != 8 || len(res.Failed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if list, _ := m.List(); len(list) != 1 {
		t.Fatalf("no copy before the import: %+v", list)
	}
	tiles, _ := db.Tiles(ctx)
	if tiles[0].Description != "graphs" || !strings.Contains(tiles[0].URL, "hunter2") || !strings.HasPrefix(tiles[0].Icon, "upload:") {
		t.Errorf("%+v", tiles[0])
	}
	list, _ := db.Profiles(ctx)
	for _, x := range list {
		if x.Name == "Play" && x.AutoGame {
			t.Error("an import armed a game trigger")
		}
		if x.Name == "Work" && !x.AutoGame {
			t.Error("an import dropped a game trigger")
		}
	}
	v, _, _ := db.Setting(ctx, "home.layout")
	if string(v) != `{"order":["cpu"],"hidden":[]}` {
		t.Error(string(v))
	}
	if on, _ := m.cfg.Weekly.Enabled(ctx); !on {
		t.Error("weekly")
	}
	again, err := m.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = m.plan(ctx, importReq(dump(t, again))); err != nil || len(p.Items) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestImportBlocksWhatDoesNotValidate(t *testing.T) {
	m, db, _ := fixture(t)
	ctx := context.Background()
	id, err := db.AddProfile(ctx, store.Profile{Name: "On", Items: `[]`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE profiles SET activated_at = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	file := File{Format: format, Version: version,
		Settings: map[string]json.RawMessage{
			"secret.thing":   json.RawMessage(`1`),
			"home.layout":    json.RawMessage(`null`),
			"apps.favorites": json.RawMessage(`{"ids":["a/b"]}`),
		},
		AlertRules: []Rule{
			{Name: "x", Metric: "no.such", Op: ">", Severity: "info"},
			{Name: "ok", Metric: "cpu.usage", Op: ">", Threshold: 1, Severity: "info"},
			{Name: "ok", Metric: "cpu.usage", Op: ">", Threshold: 2, Severity: "info"},
		},
		Tiles: []Tile{
			{Name: "js", URL: "javascript:alert(1)"},
			{Name: "cred", URL: "http://me:pw@a.lan/"}, // #nosec G101 -- the refusal under test
			{Name: "icon", URL: "http://a.lan/", Icon: "upload:" + strings.Repeat("b", 64)},
			{Name: "svg", URL: "http://a.lan/", Icon: "<svg>"},
		},
		Profiles: []Profile{
			{Name: "bad"},
			{Name: "On", Items: []profiles.Item{{Kind: "app", Target: "a", Do: "start"}}},
		},
	}
	p, err := m.plan(ctx, importReq(dump(t, file)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != "rule:ok" {
		t.Fatalf("%+v", p.Items)
	}
	if len(p.Blocked) != 11 {
		t.Fatalf("%d: %+v", len(p.Blocked), p.Blocked)
	}
}

func TestParseRefuses(t *testing.T) {
	for _, s := range []string{
		"", "null", "[]", "{}", "not json",
		`{"format":"other","version":1}`,
		`{"format":"hearth-settings","version":2}`,
		`{"format":"hearth-settings","version":1,"token":"x"}`,
		`{"format":"hearth-settings","version":1,"profiles":[{"name":"a","items":[],"auto_game":true}]}`,
		`{"format":"hearth-settings","version":1,"channels":{}}`,
		`{"format":"hearth-settings","version":1} {"format":"hearth-settings","version":1}`,
		`{"format":"hearth-settings","version":1} trailing`,
	} {
		if _, err := parse(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if _, err := parse(`{"format":"hearth-settings","version":1}`); err != nil {
		t.Error(err)
	}
}

// Without the way back, an import changes nothing.
func TestNoCopyNoImport(t *testing.T) {
	m, db, _ := fixture(t)
	ctx := context.Background()
	f := File{Format: format, Version: version, Tiles: []Tile{{Name: "A", URL: "http://a.lan/"}}}
	p, err := m.plan(ctx, importReq(dump(t, f)))
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.cfg.Dir = filepath.Join(blocker, "backups")
	if _, err := m.run(ctx, actions.Request{}, p); err == nil {
		t.Fatal("ran without a copy")
	}
	if tiles, _ := db.Tiles(ctx); len(tiles) != 0 {
		t.Fatalf("%+v", tiles)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(`{"format":"hearth-settings","version":1}`)
	f.Add(`{"format":"hearth-settings","version":1,"settings":{"a":1},"tiles":[{"name":"x"}]}`)
	f.Fuzz(func(t *testing.T, s string) {
		got, err := parse(s)
		if err == nil && (got.Format != format || got.Version != version) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzCleanURL(f *testing.F) {
	f.Add("http://a.lan/?token=x#y")
	f.Add("://%zz")
	f.Fuzz(func(t *testing.T, s string) {
		out := cleanURL(s)
		if cleanURL(out) != out {
			t.Fatalf("%q: %q then %q", s, out, cleanURL(out))
		}
	})
}
