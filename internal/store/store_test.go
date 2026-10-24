package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hearth", "hearth.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOpenPermissionsAndWAL(t *testing.T) {
	db, path := openTemp(t)
	ctx := context.Background()

	if err := db.Audit(ctx, AuditEntry{Origin: OriginLocal, Action: "test.write", Target: "x", Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	var jm string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&jm); err != nil {
		t.Fatal(err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal", jm)
	}
	if m := mode(t, filepath.Dir(path)); m != 0o700 {
		t.Errorf("dir mode %o, want 700", m)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if m := mode(t, p); m != 0o600 {
			t.Errorf("%s mode %o, want 600", filepath.Base(p), m)
		}
	}
}

func TestOpenTightensExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hearth")
	path := filepath.Join(dir, "hearth.db")
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- deliberately loose, Open must fix it
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil { // #nosec G306 G703 -- deliberately loose, under t.TempDir()
		t.Fatal(err)
	}
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %o, want 700", m)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("file mode %o, want 600", m)
	}
}

func TestOpenRejectsSymlinkAndRelative(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil { // #nosec G703 -- under t.TempDir()
		t.Fatal(err)
	}
	link := filepath.Join(dir, "hearth.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), link); err == nil {
		t.Error("opened a symlinked database")
	}
	if _, err := Open(context.Background(), "hearth.db"); err == nil {
		t.Error("opened a relative path")
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	_, path := openTemp(t)
	for range 2 {
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if want, _ := loadMigrations(migrationFiles); n != len(want) {
			t.Fatalf("schema_migrations has %d rows, want %d", n, len(want))
		}
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	bad := map[string]fstest.MapFS{
		"gap":       {"0001_a.sql": file(""), "0003_c.sql": file("")},
		"duplicate": {"0001_a.sql": file(""), "0001_b.sql": file("")},
		"not one":   {"0002_a.sql": file("")},
		"bad name":  {"0001_a.sql": file(""), "2_b.sql": file("")},
		"uppercase": {"0001_A.sql": file("")},
	}
	for name, fsys := range bad {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ms, err := loadMigrations(fstest.MapFS{"0002_b.sql": file("B"), "0001_a.sql": file("A")})
	if err != nil || len(ms) != 2 || ms[0].sql != "A" || ms[1].version != 2 {
		t.Fatalf("got %+v, %v", ms, err)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	// The real migrations are already applied and are skipped; the two
	// test migrations come after them.
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, m := range ms {
		fsys[m.name] = &fstest.MapFile{Data: []byte("SELECT 1")}
	}
	n := len(ms)
	okName, brokenName := fmt.Sprintf("%04d_ok.sql", n+1), fmt.Sprintf("%04d_broken.sql", n+2)
	fsys[okName] = &fstest.MapFile{Data: []byte("CREATE TABLE t2 (x INTEGER)")}
	fsys[brokenName] = &fstest.MapFile{Data: []byte("CREATE TABLE t3 (x INTEGER); NOT SQL")}
	if err := db.migrate(ctx, fsys); err == nil || !strings.Contains(err.Error(), brokenName) {
		t.Fatalf("got %v, want failure in %s", err, brokenName)
	}
	var v int
	if err := db.QueryRowContext(context.Background(), `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != n+1 {
		t.Fatalf("version %d (%v), want %d", v, err, n+1)
	}
	if _, err := db.ExecContext(context.Background(), `SELECT * FROM t3`); err == nil {
		t.Error("table from failed migration exists")
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO schema_migrations VALUES (999, 'future.sql', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(context.Background(), migrationFiles); err == nil {
		t.Fatal("accepted a schema newer than the binary")
	}
}

func TestSettings(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if _, ok, err := db.Setting(ctx, "home.layout"); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	for _, v := range []string{`{"order":["cpu"]}`, `{"order":["mem"]}`} {
		if err := db.SetSetting(ctx, "home.layout", []byte(v)); err != nil {
			t.Fatal(err)
		}
		got, ok, err := db.Setting(ctx, "home.layout")
		if !ok || err != nil || string(got) != v {
			t.Fatalf("got %s %v %v", got, ok, err)
		}
	}
	if err := db.SetSetting(ctx, "home.layout", []byte(`{nope`)); err == nil {
		t.Fatal("invalid JSON stored")
	}
}
