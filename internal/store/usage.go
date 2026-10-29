package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ItemUsage is one item_usage row.
type ItemUsage struct {
	Item   string    `json:"item"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	Time   time.Time `json:"t"`
	CPUAvg float64   `json:"cpu_avg"`
	CPUMax float64   `json:"cpu_max"`
	MemAvg float64   `json:"mem_avg"`
	MemMax float64   `json:"mem_max"`
	N      int       `json:"n"`
}

// AddItemUsage stores hourly rows. An hour already stored (hearthd
// restarted within it) is merged, weighted by sample count.
func (db *DB) AddItemUsage(ctx context.Context, rows []ItemUsage) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_usage (item, ts, kind, name, cpu_avg, cpu_max, mem_avg, mem_max, n)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (item, ts) DO UPDATE SET
					cpu_avg = (cpu_avg * n + excluded.cpu_avg * excluded.n) / (n + excluded.n),
					mem_avg = (mem_avg * n + excluded.mem_avg * excluded.n) / (n + excluded.n),
					cpu_max = MAX(cpu_max, excluded.cpu_max), mem_max = MAX(mem_max, excluded.mem_max),
					n = n + excluded.n, name = excluded.name`,
				r.Item, r.Time.Unix(), r.Kind, r.Name, r.CPUAvg, r.CPUMax, r.MemAvg, r.MemMax, r.N); err != nil {
				return fmt.Errorf("store: add item usage: %w", err)
			}
		}
		return nil
	})
}

// PruneItemUsage deletes hours before t.
func (db *DB) PruneItemUsage(ctx context.Context, before time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM item_usage WHERE ts < ?`, before.Unix())
	return err
}

// ItemHistory returns item's hours in [from, to), oldest first.
func (db *DB) ItemHistory(ctx context.Context, item string, from, to time.Time) ([]ItemUsage, error) {
	rows, err := db.QueryContext(ctx, `SELECT item, ts, kind, name, cpu_avg, cpu_max, mem_avg, mem_max, n
		FROM item_usage WHERE item = ? AND ts >= ? AND ts < ? ORDER BY ts`, item, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: item history: %w", err)
	}
	return scanUsage(rows)
}

// ItemTotal is one item's usage over a period.
type ItemTotal struct {
	Item   string  `json:"item"`
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	CPUAvg float64 `json:"cpu_avg"` // over the hours it was recorded
	CPUMax float64 `json:"cpu_max"`
	// Busy is the CPU time used, in hours of the whole machine (times
	// the core count for CPU-hours).
	Busy   float64 `json:"busy_hours"`
	MemAvg float64 `json:"mem_avg"`
	MemMax float64 `json:"mem_max"`
	Hours  int     `json:"hours"` // hours with a row
}

// TopItems returns the items with the most CPU time in [from, to), then
// by memory, at most limit.
func (db *DB) TopItems(ctx context.Context, from, to time.Time, limit int) ([]ItemTotal, error) {
	rows, err := db.QueryContext(ctx, `SELECT item, kind, (SELECT name FROM item_usage u2 WHERE u2.item = u.item ORDER BY ts DESC LIMIT 1),
			SUM(cpu_avg * n) / SUM(n), MAX(cpu_max), SUM(cpu_avg * n) / 60.0 / 100.0,
			SUM(mem_avg * n) / SUM(n), MAX(mem_max), COUNT(*)
		FROM item_usage u WHERE ts >= ? AND ts < ? GROUP BY item, kind
		ORDER BY SUM(cpu_avg * n) DESC, MAX(mem_max) DESC LIMIT ?`, from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: top items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ItemTotal{}
	for rows.Next() {
		var t ItemTotal
		if err := rows.Scan(&t.Item, &t.Kind, &t.Name, &t.CPUAvg, &t.CPUMax, &t.Busy, &t.MemAvg, &t.MemMax, &t.Hours); err != nil {
			return nil, fmt.Errorf("store: top items: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanUsage(rows *sql.Rows) ([]ItemUsage, error) {
	defer func() { _ = rows.Close() }()
	out := []ItemUsage{}
	for rows.Next() {
		var r ItemUsage
		var ts int64
		if err := rows.Scan(&r.Item, &ts, &r.Kind, &r.Name, &r.CPUAvg, &r.CPUMax, &r.MemAvg, &r.MemMax, &r.N); err != nil {
			return nil, fmt.Errorf("store: item usage: %w", err)
		}
		r.Time = time.Unix(ts, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditRow is one audit_log row as reports show it.
type AuditRow struct {
	Time   time.Time `json:"t"`
	Origin string    `json:"origin"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Result string    `json:"result"`
}

// AuditBetween returns the audit log in [from, to), newest first, at most limit.
func (db *DB) AuditBetween(ctx context.Context, from, to time.Time, limit int) ([]AuditRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT ts, origin, action, target, result FROM audit_log
		WHERE ts >= ? AND ts < ? ORDER BY ts DESC, id DESC LIMIT ?`, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		var ms int64
		if err := rows.Scan(&ms, &r.Origin, &r.Action, &r.Target, &r.Result); err != nil {
			return nil, fmt.Errorf("store: audit: %w", err)
		}
		r.Time = time.UnixMilli(ms)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AlertEventsBetween returns the incidents that started in [from, to),
// newest first, at most limit.
func (db *DB) AlertEventsBetween(ctx context.Context, from, to time.Time, limit int) ([]AlertEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+eventCols+` FROM alert_events
		WHERE started_at >= ? AND started_at < ? ORDER BY started_at DESC, id DESC LIMIT ?`, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: alert events: %w", err)
	}
	return scanEvents(rows)
}
