package store

import (
	"context"
	"fmt"
)

// maxLynisRuns bounds the history: years of a weekly schedule.
const maxLynisRuns = 500

// LynisRun is one finished audit's figures.
type LynisRun struct {
	Time        int64 `json:"time"` // unix ms
	Index       int   `json:"index"`
	Warnings    int   `json:"warnings"`
	Suggestions int   `json:"suggestions"`
}

// AddLynisRun stores a run, once, and drops the oldest past the bound.
func (db *DB) AddLynisRun(ctx context.Context, r LynisRun) error {
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO lynis_runs (time, hardening, warnings, suggestions) VALUES (?, ?, ?, ?)`,
		r.Time, r.Index, r.Warnings, r.Suggestions); err != nil {
		return fmt.Errorf("store: lynis run: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM lynis_runs WHERE time NOT IN (SELECT time FROM lynis_runs ORDER BY time DESC LIMIT ?)`, maxLynisRuns); err != nil {
		return fmt.Errorf("store: prune lynis runs: %w", err)
	}
	return nil
}

// LynisRuns returns up to limit runs, newest first.
func (db *DB) LynisRuns(ctx context.Context, limit int) ([]LynisRun, error) {
	rows, err := db.QueryContext(ctx, `SELECT time, hardening, warnings, suggestions FROM lynis_runs ORDER BY time DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: lynis runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []LynisRun{}
	for rows.Next() {
		var r LynisRun
		if err := rows.Scan(&r.Time, &r.Index, &r.Warnings, &r.Suggestions); err != nil {
			return nil, fmt.Errorf("store: lynis runs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
