package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// VacuumInto writes a consistent copy of the database to path, which must
// not exist.
func (db *DB) VacuumInto(ctx context.Context, path string) error {
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("store: vacuum into: %w", err)
	}
	return nil
}

// Setting returns the JSON stored under key; ok is false if none is.
func (db *DB) Setting(ctx context.Context, key string) (value json.RawMessage, ok bool, err error) {
	var s string
	err = db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&s)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("store: setting %s: %w", key, err)
	}
	return json.RawMessage(s), true, nil
}

// SetSetting stores value (valid JSON) under key, replacing any previous one.
func (db *DB) SetSetting(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return fmt.Errorf("store: setting %s: invalid JSON", key)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(value), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: set setting %s: %w", key, err)
	}
	return nil
}
