package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tile is one homepage_tiles row.
type Tile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Icon        string `json:"icon"`  // "builtin:<name>", "upload:<sha256>" or ""
	Group       string `json:"group"` // "" for none
	Container   string `json:"container"`
	HealthCheck bool   `json:"health_check"`
	Position    int64  `json:"position"`
}

const tileCols = `id, name, url, description, icon, grp, container, health_check, position`

// Tiles returns all tiles in page order.
func (db *DB) Tiles(ctx context.Context) ([]Tile, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+tileCols+` FROM homepage_tiles ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("store: tiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Tile{}
	for rows.Next() {
		var t Tile
		if err := rows.Scan(&t.ID, &t.Name, &t.URL, &t.Description, &t.Icon, &t.Group, &t.Container, &t.HealthCheck, &t.Position); err != nil {
			return nil, fmt.Errorf("store: tiles: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTile inserts t at the end of the page and returns its ID.
func (db *DB) CreateTile(ctx context.Context, t Tile) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO homepage_tiles (name, url, description, icon, grp, container, health_check, position, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM homepage_tiles), ?)`,
		t.Name, t.URL, t.Description, t.Icon, t.Group, t.Container, t.HealthCheck, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: create tile: %w", err)
	}
	return res.LastInsertId()
}

// UpdateTile replaces tile t.ID (its position stays) and deletes an
// uploaded icon it no longer uses.
func (db *DB) UpdateTile(ctx context.Context, t Tile) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT icon FROM homepage_tiles WHERE id = ?`, t.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE homepage_tiles SET name = ?, url = ?, description = ?, icon = ?, grp = ?, container = ?, health_check = ?, updated_at = ?
			 WHERE id = ?`,
			t.Name, t.URL, t.Description, t.Icon, t.Group, t.Container, t.HealthCheck, time.Now().UnixMilli(), t.ID); err != nil {
			return err
		}
		return dropUnusedIcon(ctx, tx, old)
	})
}

// DeleteTile deletes tile id and an uploaded icon no other tile uses.
func (db *DB) DeleteTile(ctx context.Context, id int64) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		var icon string
		if err := tx.QueryRowContext(ctx, `SELECT icon FROM homepage_tiles WHERE id = ?`, id).Scan(&icon); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM homepage_tiles WHERE id = ?`, id); err != nil {
			return err
		}
		return dropUnusedIcon(ctx, tx, icon)
	})
}

// OrderTiles sets the page order: ids first, in that order, then any
// tile not listed, in its old order.
func (db *DB) OrderTiles(ctx context.Context, ids []int64) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		// Unlisted tiles move behind the listed ones, keeping their order.
		if _, err := tx.ExecContext(ctx, `UPDATE homepage_tiles SET position = position + ?`, len(ids)); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE homepage_tiles SET position = ? WHERE id = ?`, i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func dropUnusedIcon(ctx context.Context, tx *sql.Tx, icon string) error {
	sha, ok := strings.CutPrefix(icon, "upload:")
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM homepage_icons WHERE sha = ? AND NOT EXISTS (SELECT 1 FROM homepage_tiles WHERE icon = ?)`, sha, icon)
	return err
}

// PutIcon stores an icon (already checked) under sha; storing the same
// icon twice is fine.
func (db *DB) PutIcon(ctx context.Context, sha, mime string, data []byte) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO homepage_icons (sha, mime, data, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (sha) DO NOTHING`,
		sha, mime, data, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put icon: %w", err)
	}
	return nil
}

// Icon returns a stored icon.
func (db *DB) Icon(ctx context.Context, sha string) (mime string, data []byte, err error) {
	err = db.QueryRowContext(ctx, `SELECT mime, data FROM homepage_icons WHERE sha = ?`, sha).Scan(&mime, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return mime, data, err
}

// DropOrphanIcons deletes icons uploaded more than age ago that no tile
// uses (an upload whose form was never saved).
func (db *DB) DropOrphanIcons(ctx context.Context, age time.Duration) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM homepage_icons WHERE created_at < ? AND NOT EXISTS
		 (SELECT 1 FROM homepage_tiles WHERE icon = 'upload:' || homepage_icons.sha)`,
		time.Now().Add(-age).UnixMilli())
	return err
}

// inTx runs fn in a transaction, committed if fn returns nil.
func (db *DB) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
