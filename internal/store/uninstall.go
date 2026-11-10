package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// UninstallOp is one uninstall operation (uninstall_ops row).
type UninstallOp struct {
	ID      string          `json:"id"`
	Item    string          `json:"item"`
	Name    string          `json:"name"`
	Created int64           `json:"created"`
	Updated int64           `json:"updated"`
	State   string          `json:"state"`
	Record  json.RawMessage `json:"record"`
}

// SaveUninstallOp inserts or replaces op.
func (db *DB) SaveUninstallOp(ctx context.Context, op UninstallOp) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO uninstall_ops (id, item, name, created, updated, state, record) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET updated = excluded.updated, state = excluded.state, record = excluded.record`,
		op.ID, op.Item, op.Name, op.Created, op.Updated, op.State, string(op.Record))
	if err != nil {
		return fmt.Errorf("store: save uninstall: %w", err)
	}
	return nil
}

// UninstallOps returns the operations created since since, newest first.
func (db *DB) UninstallOps(ctx context.Context, since time.Time) ([]UninstallOp, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, item, name, created, updated, state, record FROM uninstall_ops WHERE created >= ? ORDER BY created DESC LIMIT 500`,
		since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: uninstalls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []UninstallOp
	for rows.Next() {
		var op UninstallOp
		var rec string
		if err := rows.Scan(&op.ID, &op.Item, &op.Name, &op.Created, &op.Updated, &op.State, &rec); err != nil {
			return nil, fmt.Errorf("store: uninstalls: %w", err)
		}
		op.Record = json.RawMessage(rec)
		out = append(out, op)
	}
	return out, rows.Err()
}

// GetUninstallOp returns one operation, or false.
func (db *DB) GetUninstallOp(ctx context.Context, id string) (UninstallOp, bool, error) {
	var op UninstallOp
	var rec string
	err := db.QueryRowContext(ctx, `SELECT id, item, name, created, updated, state, record FROM uninstall_ops WHERE id = ?`, id).
		Scan(&op.ID, &op.Item, &op.Name, &op.Created, &op.Updated, &op.State, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		return op, false, nil
	}
	if err != nil {
		return op, false, fmt.Errorf("store: uninstall: %w", err)
	}
	op.Record = json.RawMessage(rec)
	return op, true, nil
}

// PruneUninstallOps removes operations created before before.
func (db *DB) PruneUninstallOps(ctx context.Context, before time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM uninstall_ops WHERE created < ?`, before.UnixMilli()); err != nil {
		return fmt.Errorf("store: prune uninstalls: %w", err)
	}
	return nil
}

// Playtime is one game's tracked time.
type Playtime struct {
	ID         string `json:"id"`
	Seconds    int64  `json:"seconds"`
	LastPlayed int64  `json:"last_played"`
}

// TakePlaytime removes game id's tracked time and returns it, or false.
func (db *DB) TakePlaytime(ctx context.Context, id string) (Playtime, bool, error) {
	p := Playtime{ID: id}
	err := db.QueryRowContext(ctx, `DELETE FROM game_playtime WHERE id = ? RETURNING seconds, last_played`, id).Scan(&p.Seconds, &p.LastPlayed)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("store: take playtime: %w", err)
	}
	return p, true, nil
}

// PutPlaytime puts back a game's tracked time, adding to any tracked since.
func (db *DB) PutPlaytime(ctx context.Context, p Playtime) error {
	if p.Seconds < 0 {
		return errors.New("store: put playtime: negative seconds")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO game_playtime (id, seconds, last_played) VALUES (?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET seconds = seconds + excluded.seconds, last_played = MAX(last_played, excluded.last_played)`,
		p.ID, p.Seconds, p.LastPlayed)
	if err != nil {
		return fmt.Errorf("store: put playtime: %w", err)
	}
	return nil
}
