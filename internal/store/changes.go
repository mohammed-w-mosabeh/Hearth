package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Snapshot is one snapshots row; Data is only filled by Snapshot.
type Snapshot struct {
	ID    int64     `json:"id"`
	Time  time.Time `json:"t"`
	Label string    `json:"label"`
	Auto  bool      `json:"auto"`
	Data  []byte    `json:"-"`
	Size  int       `json:"size"`
}

// Change is one changes row.
type Change struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"t"`
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	Before  string    `json:"before,omitempty"`
	After   string    `json:"after,omitempty"`
}

// AddSnapshot stores s and the changes seen with it, in one transaction.
func (db *DB) AddSnapshot(ctx context.Context, s Snapshot, changes []Change) (int64, error) {
	var id int64
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO snapshots (ts, label, auto, data) VALUES (?, ?, ?, ?)`,
			s.Time.UnixMilli(), s.Label, s.Auto, s.Data)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, c := range changes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO changes (ts, source, kind, subject, before, after) VALUES (?, ?, ?, ?, ?, ?)`,
				c.Time.UnixMilli(), c.Source, c.Kind, c.Subject, c.Before, c.After); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: add snapshot: %w", err)
	}
	return id, nil
}

// AddChange records one change that no snapshot found: a boot, a restart.
func (db *DB) AddChange(ctx context.Context, c Change) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO changes (ts, source, kind, subject, before, after) VALUES (?, ?, ?, ?, ?, ?)`,
		c.Time.UnixMilli(), c.Source, c.Kind, c.Subject, c.Before, c.After); err != nil {
		return fmt.Errorf("store: add change: %w", err)
	}
	return nil
}

// Snapshots lists the snapshots, newest first, without their data.
func (db *DB) Snapshots(ctx context.Context) ([]Snapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, ts, label, auto, length(data) FROM snapshots ORDER BY ts DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		var ms int64
		if err := rows.Scan(&s.ID, &ms, &s.Label, &s.Auto, &s.Size); err != nil {
			return nil, fmt.Errorf("store: snapshots: %w", err)
		}
		s.Time = time.UnixMilli(ms)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Snapshot returns snapshot id with its data.
func (db *DB) Snapshot(ctx context.Context, id int64) (Snapshot, error) {
	var s Snapshot
	var ms int64
	err := db.QueryRowContext(ctx, `SELECT id, ts, label, auto, data FROM snapshots WHERE id = ?`, id).
		Scan(&s.ID, &ms, &s.Label, &s.Auto, &s.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, fmt.Errorf("store: snapshot: %w", err)
	}
	s.Time, s.Size = time.UnixMilli(ms), len(s.Data)
	return s, nil
}

// LatestAutoSnapshot returns the newest automatic snapshot, or ErrNotFound.
func (db *DB) LatestAutoSnapshot(ctx context.Context) (Snapshot, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM snapshots WHERE auto = 1 ORDER BY ts DESC, id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: snapshot: %w", err)
	}
	return db.Snapshot(ctx, id)
}

// DeleteSnapshot deletes snapshot id.
func (db *DB) DeleteSnapshot(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete snapshot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneSnapshots deletes automatic snapshots before t, keeping the newest
// automatic one (the base for the next diff), and changes before t.
func (db *DB) PruneSnapshots(ctx context.Context, before time.Time) error {
	ms := before.UnixMilli()
	if _, err := db.ExecContext(ctx, `DELETE FROM snapshots WHERE auto = 1 AND ts < ?
		AND id <> (SELECT id FROM snapshots WHERE auto = 1 ORDER BY ts DESC, id DESC LIMIT 1)`, ms); err != nil {
		return fmt.Errorf("store: prune snapshots: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM changes WHERE ts < ?`, ms); err != nil {
		return fmt.Errorf("store: prune changes: %w", err)
	}
	return nil
}

// ChangesBetween returns changes in [from, to) from the given sources (all
// if empty), newest first, at most limit.
func (db *DB) ChangesBetween(ctx context.Context, from, to time.Time, sources []string, limit int) ([]Change, error) {
	set := ""
	if len(sources) > 0 {
		set = "," + strings.Join(sources, ",") + ","
	}
	rows, err := db.QueryContext(ctx, `SELECT id, ts, source, kind, subject, before, after FROM changes
		WHERE ts >= ?1 AND ts < ?2 AND (?3 = '' OR instr(?3, ',' || source || ',') > 0)
		ORDER BY ts DESC, id DESC LIMIT ?4`, from.UnixMilli(), to.UnixMilli(), set, limit)
	if err != nil {
		return nil, fmt.Errorf("store: changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Change{}
	for rows.Next() {
		var c Change
		var ms int64
		if err := rows.Scan(&c.ID, &ms, &c.Source, &c.Kind, &c.Subject, &c.Before, &c.After); err != nil {
			return nil, fmt.Errorf("store: changes: %w", err)
		}
		c.Time = time.UnixMilli(ms)
		out = append(out, c)
	}
	return out, rows.Err()
}
