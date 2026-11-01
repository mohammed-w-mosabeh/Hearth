package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// NetUsage is one interface's bytes on one local day.
type NetUsage struct {
	Iface string `json:"iface"`
	Day   string `json:"day"`
	Rx    int64  `json:"rx"`
	Tx    int64  `json:"tx"`
}

// Outage is one stretch without internet; EndedAt is nil while it lasts.
type Outage struct {
	ID        int64  `json:"id"`
	StartedAt int64  `json:"started_at"`
	EndedAt   *int64 `json:"ended_at,omitempty"`
	Cause     string `json:"cause"`
}

// AddNetUsage adds bytes to their days.
func (db *DB) AddNetUsage(ctx context.Context, rows []NetUsage) error {
	if len(rows) == 0 {
		return nil
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO net_usage (iface, day, rx, tx) VALUES (?, ?, ?, ?)
				ON CONFLICT (iface, day) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`, r.Iface, r.Day, r.Rx, r.Tx); err != nil {
				return fmt.Errorf("store: net usage: %w", err)
			}
		}
		return nil
	})
}

// NetUsageSince returns the days from day on, oldest first.
func (db *DB) NetUsageSince(ctx context.Context, day string) ([]NetUsage, error) {
	rows, err := db.QueryContext(ctx, `SELECT iface, day, rx, tx FROM net_usage WHERE day >= ? ORDER BY day, iface`, day)
	if err != nil {
		return nil, fmt.Errorf("store: net usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []NetUsage{}
	for rows.Next() {
		var u NetUsage
		if err := rows.Scan(&u.Iface, &u.Day, &u.Rx, &u.Tx); err != nil {
			return nil, fmt.Errorf("store: net usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// OpenOutage records the start of an outage.
func (db *DB) OpenOutage(ctx context.Context, at time.Time, cause string) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO net_outages (started_at, cause) VALUES (?, ?)`, at.UnixMilli(), cause)
	if err != nil {
		return 0, fmt.Errorf("store: outage: %w", err)
	}
	return res.LastInsertId()
}

// CloseOutages ends every open outage, also one left by a restart.
func (db *DB) CloseOutages(ctx context.Context, at time.Time) error {
	if _, err := db.ExecContext(ctx, `UPDATE net_outages SET ended_at = ? WHERE ended_at IS NULL`, at.UnixMilli()); err != nil {
		return fmt.Errorf("store: outage: %w", err)
	}
	return nil
}

// Outages lists the newest outages.
func (db *DB) Outages(ctx context.Context, limit int) ([]Outage, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, started_at, ended_at, cause FROM net_outages ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: outages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Outage{}
	for rows.Next() {
		var o Outage
		if err := rows.Scan(&o.ID, &o.StartedAt, &o.EndedAt, &o.Cause); err != nil {
			return nil, fmt.Errorf("store: outages: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PruneConnectivity removes what is older than the cutoff.
func (db *DB) PruneConnectivity(ctx context.Context, before time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM net_usage WHERE day < ?`, before.Format(time.DateOnly)); err != nil {
		return fmt.Errorf("store: prune usage: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM net_outages WHERE ended_at IS NOT NULL AND started_at < ?`, before.UnixMilli()); err != nil {
		return fmt.Errorf("store: prune outages: %w", err)
	}
	return nil
}
