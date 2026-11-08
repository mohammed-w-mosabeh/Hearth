package modes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newProfileStore(t *testing.T, db *store.DB) (*ProfileStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "modes")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	set := NewSet(Config{Dir: dir, Store: db})
	set.Load(context.Background())
	return &ProfileStore{Set: set, DB: db}, dir
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Work":                   "work",
		"Dev Box!":               "dev-box",
		"  --Gaming  night-- ":   "gaming-night",
		"وضع":                    "mode",
		"../../etc":              "etc",
		strings.Repeat("ab", 30): strings.Repeat("ab", 17),
	} {
		if got := Slug(in); got != want || !ValidID(got) {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	m, err := Parse("work", []byte(work))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse("work", raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(back)
	if strings.Replace(string(a), m.Hash, "", 1) != strings.Replace(string(b), back.Hash, "", 1) {
		t.Fatalf("%s\n%s", a, b)
	}
}

func TestProfileStoreKeepsTheRestOfTheMode(t *testing.T) {
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	ctx := context.Background()
	write(t, dir, "work.toml", work)
	ps.Set.Load(ctx)
	list, err := ps.Profiles(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Work" || list[0].AutoGame {
		// A game trigger written by hand is not armed until a click.
		t.Fatalf("%v %+v", err, list)
	}
	p := list[0]
	p.Name, p.AutoGame = "Office", true
	p.Items = `[{"kind":"stack","target":"notes","do":"start"},{"kind":"stack","target":"photos","do":"stop"},{"kind":"app","target":"org.gnome.Calculator","do":"start"}]`
	if err := ps.UpdateProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	m, ok := ps.Set.Mode("work")
	if !ok || m.Name != "Office" || len(m.Items) != 0 || len(m.Session.Open) != 1 || m.Session.PowerProfile != "balanced" ||
		len(m.Stacks) != 2 || len(m.Triggers) != 5 {
		t.Fatalf("%+v", m)
	}
	// Saving with the game trigger on is the click that arms it, for this file.
	if got, _ := ps.Profile(ctx, p.ID); !got.AutoGame {
		t.Fatalf("%+v", got)
	}
	// Turning it off removes the game trigger and keeps the others.
	p.AutoGame = false
	if err := ps.UpdateProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	if m, _ := ps.Set.Mode("work"); m.HasGame() || len(m.Triggers) != 4 {
		t.Fatalf("%+v", m.Triggers)
	}
}

func TestProfileStoreRefusesAStaleEdit(t *testing.T) {
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	ctx := context.Background()
	id, err := ps.AddProfile(ctx, store.Profile{Name: "Quiet", Items: `[{"kind":"app","target":"a","do":"start"}]`})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ps.Profile(ctx, id)
	// The user edits the file; Hearth has not read it again yet.
	write(t, dir, "quiet.toml", "name = \"Quiet\"\n[session]\nbrightness = 40\n")
	p.Name = "Calm"
	if err := ps.UpdateProfile(ctx, p); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if err := ps.DeleteProfile(ctx, id); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "quiet.toml")) // #nosec G304 -- a test's temporary folder
	if !strings.Contains(string(raw), "brightness = 40") {
		t.Fatalf("the user's edit was overwritten: %s", raw)
	}
	// Two profiles with one name keep apart files.
	if _, err := ps.AddProfile(ctx, store.Profile{Name: "quiet!", Items: `[{"kind":"app","target":"b","do":"start"}]`}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ps.Set.Mode("quiet-2"); !ok {
		t.Fatalf("%+v %+v", ps.Set.Modes(), ps.Set.Errors())
	}
}

func TestProfileStoreDelete(t *testing.T) {
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	ctx := context.Background()
	id, err := ps.AddProfile(ctx, store.Profile{Name: "Gone", Items: `[{"kind":"app","target":"a","do":"start"}]`, AutoGame: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.DeleteProfile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Profile(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, deletedDir, "gone-*.toml"))
	if len(kept) != 1 {
		t.Fatalf("not kept in %s: %v", deletedDir, kept)
	}
	if a, _ := db.ModeArmings(ctx); len(a) != 0 {
		t.Fatalf("still armed: %+v", a)
	}
	// A new mode of the same name is not the deleted one in history.
	id2, err := ps.AddProfile(ctx, store.Profile{Name: "Gone", Items: `[{"kind":"app","target":"a","do":"start"}]`})
	if err != nil || id2 == id {
		t.Fatal(id, id2, err)
	}
	if _, ok := ps.Set.Mode("gone-2"); !ok {
		t.Fatalf("%+v", ps.Set.Modes())
	}
}

// A mode active when its file goes stays listed, so it can be restored.
func TestProfileStoreKeepsAnOrphanActive(t *testing.T) {
	db := openDB(t)
	ps, dir := newProfileStore(t, db)
	ctx := context.Background()
	id, _ := ps.AddProfile(ctx, store.Profile{Name: "Work", Items: `[{"kind":"unit","target":"user/a.service","do":"stop"}]`})
	at := time.UnixMilli(1793359297763)
	undo := `[{"kind":"unit","target":"user/a.service","do":"start"}]`
	if err := ps.SetProfileActive(ctx, id, at, "user", undo); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "work.toml")); err != nil {
		t.Fatal(err)
	}
	ps.Set.Load(ctx)
	p, err := ps.Profile(ctx, id)
	if err != nil || p.Name != "work" || p.Items != "[]" || p.Undo != undo || !p.ActivatedAt.Equal(at) {
		t.Fatalf("%v %+v", err, p)
	}
	if err := ps.UpdateProfile(ctx, store.Profile{ID: id, Name: "x", Items: "[]"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := ps.ClearProfileActive(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Profile(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMigrate(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	at := time.UnixMilli(1793359297763)
	dev, _ := db.AddProfile(ctx, store.Profile{Name: "Dev", Items: `[{"kind":"container","target":"postgres","do":"start"},{"kind":"app","target":"code","do":"start"}]`})
	games, _ := db.AddProfile(ctx, store.Profile{Name: "Gaming", AutoGame: true, Items: `[{"kind":"unit","target":"user/syncnotes.service","do":"stop"}]`})
	odd, _ := db.AddProfile(ctx, store.Profile{Name: " Odd\u202e", Items: `[{"kind":"app","target":"x","do":"start"}]`})
	broken, _ := db.AddProfile(ctx, store.Profile{Name: "Broken", Items: `not json`})
	undo := `[{"kind":"unit","target":"user/syncnotes.service","do":"start"}]`
	if err := db.SetProfileActive(ctx, games, at, "game", undo); err != nil {
		t.Fatal(err)
	}
	// As 0022 does on a machine that has these profiles.
	if _, err := db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = 'mode_ids'`, broken); err != nil {
		t.Fatal(err)
	}
	ps, dir := newProfileStore(t, db)
	// A crash had left Dev's file written but not adopted.
	m, _ := apply(Mode{Stacks: []StackAction{}, Triggers: []Trigger{}}, store.Profile{Name: "Dev", Items: `[{"kind":"container","target":"postgres","do":"start"},{"kind":"app","target":"code","do":"start"}]`})
	raw, _ := Encode(m)
	write(t, dir, "dev.toml", string(raw))
	// A hand-written file already uses the name "gaming".
	write(t, dir, "gaming.toml", "name = \"Mine\"\n[session]\nbrightness = 5\n")

	moved, err := Migrate(ctx, db, ps.Set, 1000)
	if moved != 3 || err == nil || !strings.Contains(err.Error(), "profile 4") {
		t.Fatal(moved, err)
	}
	// The broken profile stays where it was.
	if _, err := db.Profile(ctx, broken); err != nil {
		t.Fatal(err)
	}
	left, _ := db.Profiles(ctx)
	if len(left) != 1 {
		t.Fatalf("%+v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev-2.toml")); err == nil {
		t.Fatal("the file left by the crash was written again")
	}
	byID := map[int64]store.Profile{}
	list, err := ps.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		byID[p.ID] = p
	}
	// Profiles keep their IDs, items, game trigger and restore list.
	if p := byID[dev]; p.Name != "Dev" || !strings.Contains(p.Items, `"app"`) || p.AutoGame || !p.ActivatedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
	if p := byID[games]; p.Name != "Gaming" || !p.AutoGame || p.Undo != undo || p.ActivatedBy != "game" || !p.ActivatedAt.Equal(at) {
		t.Fatalf("%+v", p)
	}
	if p := byID[odd]; p.Name != "Odd" {
		t.Fatalf("%+v", p)
	}
	if _, ok := ps.Set.Mode("gaming-2"); !ok {
		t.Fatalf("%+v", ps.Set.Modes())
	}
	if m, _ := ps.Set.Mode("gaming"); m.Name != "Mine" {
		t.Fatal("a hand-written file was replaced")
	}
	rows, _ := db.AuditBetween(ctx, time.UnixMilli(0), time.Now().Add(time.Hour), 50)
	migrated := 0
	for _, r := range rows {
		if r.Action == "profile.migrate" {
			migrated++
		}
	}
	if migrated != 4 {
		t.Fatalf("%d audit rows", migrated)
	}
	// Running again moves nothing new and keeps failing on the broken one.
	if moved, err := Migrate(ctx, db, ps.Set, 1000); moved != 0 || err == nil {
		t.Fatal(moved, err)
	}
	// New modes are numbered after every profile.
	id, err := ps.AddProfile(ctx, store.Profile{Name: "New", Items: `[{"kind":"app","target":"y","do":"start"}]`})
	if err != nil || id <= broken {
		t.Fatal(id, err)
	}
}
