// Package store owns Hearth's SQLite database: opening it with safe
// permissions, applying migrations, and the append-only audit log.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DB wraps the Hearth database handle.
type DB struct {
	*sql.DB
}

// Open creates (if needed) and opens the database at path, enforcing mode
// 0700 on its directory and 0600 on the file (SECURITY S20), enabling WAL,
// and applying pending migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("store: path %q must be absolute", path)
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := ensureFile(path); err != nil {
		return nil, err
	}

	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()

	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db := &DB{sqldb}
	if err := db.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	if err := db.migrate(ctx, migrationFiles); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directory needs the x bit; 0700 is owner-only
		return fmt.Errorf("store: chmod %s: %w", dir, err)
	}
	return nil
}

// ensureFile creates the database file with mode 0600 before SQLite touches
// it (SQLite gives -wal/-shm files the same mode), and tightens an existing
// file. Symlinks are refused so the chmod cannot be redirected.
func ensureFile(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path is the configured DB location
		if err != nil {
			return fmt.Errorf("store: create %s: %w", path, err)
		}
		return f.Close()
	case err != nil:
		return fmt.Errorf("store: stat %s: %w", path, err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("store: %s is not a regular file", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("store: chmod %s: %w", path, err)
	}
	return nil
}
