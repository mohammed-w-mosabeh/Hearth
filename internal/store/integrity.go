package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// IntegrityEntry is one watched item as last accepted. Text is left out of
// listings: only one file's text is ever in memory at a time.
type IntegrityEntry struct {
	Path     string
	Root     string
	Kind     string
	Hash     string
	Size     int64
	MTime    int64
	CTime    int64
	Text     *string
	Accepted int64
}

// IntegrityBaseline returns every entry, without its text.
func (db *DB) IntegrityBaseline(ctx context.Context) (map[string]IntegrityEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, root, kind, hash, size, mtime, ctime, accepted FROM integrity_baseline`)
	if err != nil {
		return nil, fmt.Errorf("store: integrity baseline: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]IntegrityEntry{}
	for rows.Next() {
		var e IntegrityEntry
		if err := rows.Scan(&e.Path, &e.Root, &e.Kind, &e.Hash, &e.Size, &e.MTime, &e.CTime, &e.Accepted); err != nil {
			return nil, fmt.Errorf("store: integrity baseline: %w", err)
		}
		out[e.Path] = e
	}
	return out, rows.Err()
}

// IntegrityText returns one entry's stored text, if it has any.
func (db *DB) IntegrityText(ctx context.Context, path string) (*string, error) {
	var text sql.NullString
	err := db.QueryRowContext(ctx, `SELECT text FROM integrity_baseline WHERE path = ?`, path).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: integrity text: %w", err)
	}
	if !text.Valid {
		return nil, nil
	}
	return &text.String, nil
}

// PutIntegrity stores or replaces one entry.
func (db *DB) PutIntegrity(ctx context.Context, e IntegrityEntry) error {
	if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO integrity_baseline
		(path, root, kind, hash, size, mtime, ctime, text, accepted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Path, e.Root, e.Kind, e.Hash, e.Size, e.MTime, e.CTime, e.Text, e.Accepted); err != nil {
		return fmt.Errorf("store: put integrity: %w", err)
	}
	return nil
}

// DeleteIntegrity forgets one entry.
func (db *DB) DeleteIntegrity(ctx context.Context, path string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM integrity_baseline WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete integrity: %w", err)
	}
	return nil
}
