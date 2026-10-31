package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Profile is one profiles row; Items and Undo are JSON. ActivatedAt is
// zero when the profile is not active.
type Profile struct {
	ID          int64
	Name        string
	Items       string
	AutoGame    bool
	ActivatedAt time.Time
	ActivatedBy string
	Undo        string
}

const profileColumns = `id, name, items, auto_game, activated_at, activated_by, undo`

func scanProfile(scan func(...any) error) (Profile, error) {
	var p Profile
	var at sql.NullInt64
	if err := scan(&p.ID, &p.Name, &p.Items, &p.AutoGame, &at, &p.ActivatedBy, &p.Undo); err != nil {
		return p, err
	}
	if at.Valid {
		p.ActivatedAt = time.UnixMilli(at.Int64)
	}
	return p, nil
}

// Profiles lists the profiles by name.
func (db *DB) Profiles(ctx context.Context) ([]Profile, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+profileColumns+` FROM profiles ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: profiles: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Profile returns profile id, or ErrNotFound.
func (db *DB) Profile(ctx context.Context, id int64) (Profile, error) {
	p, err := scanProfile(db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM profiles WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("store: profile: %w", err)
	}
	return p, nil
}

// AddProfile stores a new inactive profile.
func (db *DB) AddProfile(ctx context.Context, p Profile) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO profiles (name, items, auto_game) VALUES (?, ?, ?)`, p.Name, p.Items, p.AutoGame)
	if err != nil {
		return 0, fmt.Errorf("store: add profile: %w", err)
	}
	return res.LastInsertId()
}

// UpdateProfile changes what p is; whether it is active does not change.
func (db *DB) UpdateProfile(ctx context.Context, p Profile) error {
	res, err := db.ExecContext(ctx, `UPDATE profiles SET name = ?, items = ?, auto_game = ? WHERE id = ?`, p.Name, p.Items, p.AutoGame, p.ID)
	if err != nil {
		return fmt.Errorf("store: update profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProfile deletes profile id.
func (db *DB) DeleteProfile(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProfileActive marks profile id active with what restores it.
func (db *DB) SetProfileActive(ctx context.Context, id int64, at time.Time, by, undo string) error {
	if _, err := db.ExecContext(ctx, `UPDATE profiles SET activated_at = ?, activated_by = ?, undo = ? WHERE id = ?`,
		at.UnixMilli(), by, undo, id); err != nil {
		return fmt.Errorf("store: activate profile: %w", err)
	}
	return nil
}

// ClearProfileActive marks profile id inactive.
func (db *DB) ClearProfileActive(ctx context.Context, id int64) error {
	if _, err := db.ExecContext(ctx, `UPDATE profiles SET activated_at = NULL, activated_by = '', undo = '[]' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: deactivate profile: %w", err)
	}
	return nil
}
