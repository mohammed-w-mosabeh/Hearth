package store

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// Retention per resolution (SPEC 8).
const (
	Retention1m = 7 * 24 * time.Hour
	Retention1h = 365 * 24 * time.Hour
)

// Resolution selects a downsampled table.
type Resolution string

const (
	Res1m Resolution = "1m"
	Res1h Resolution = "1h"
)

func (r Resolution) table() (string, error) {
	switch r {
	case Res1m:
		return "metrics_1m", nil
	case Res1h:
		return "metrics_1h", nil
	}
	return "", fmt.Errorf("store: unknown resolution %q", r)
}

// Point is one raw metric value.
type Point struct {
	Metric string
	Value  float64
}

// Agg is one downsampled bucket.
type Agg struct {
	Time time.Time `json:"t"`
	Avg  float64   `json:"avg"`
	Min  float64   `json:"min"`
	Max  float64   `json:"max"`
	N    int       `json:"n"`
}

type bucket struct {
	sum, min, max float64
	n             int
}

// Downsampler aggregates raw points into 1-minute rows, rolls those up into
// hourly rows, and enforces retention. It is not safe for concurrent use;
// the sampler goroutine owns it.
type Downsampler struct {
	db  *DB
	log *slog.Logger
	now func() time.Time

	minute time.Time // start of the bucket being filled
	cur    map[string]*bucket
	hour   time.Time // start of the hour last rolled up / pruned
}

// NewDownsampler returns a Downsampler and catches up on hourly rollups
// missed while hearthd was not running.
func NewDownsampler(ctx context.Context, db *DB, log *slog.Logger, now func() time.Time) (*Downsampler, error) {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	d := &Downsampler{db: db, log: log, now: now, cur: map[string]*bucket{}}
	if err := d.catchUp(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// Add records points taken at t.
func (d *Downsampler) Add(ctx context.Context, t time.Time, pts []Point) {
	m := t.Truncate(time.Minute)
	if !d.minute.IsZero() && !m.Equal(d.minute) {
		d.flush(ctx)
	}
	d.minute = m
	for _, p := range pts {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		b := d.cur[p.Metric]
		if b == nil {
			d.cur[p.Metric] = &bucket{sum: p.Value, min: p.Value, max: p.Value, n: 1}
			continue
		}
		b.sum += p.Value
		b.min = min(b.min, p.Value)
		b.max = max(b.max, p.Value)
		b.n++
	}
	if h := d.now().Truncate(time.Hour); h.After(d.hour) {
		d.hourly(ctx, h)
	}
}

// Close flushes the partially filled minute.
func (d *Downsampler) Close(ctx context.Context) {
	d.flush(ctx)
}

func (d *Downsampler) flush(ctx context.Context) {
	if len(d.cur) == 0 {
		return
	}
	if err := d.writeMinute(ctx); err != nil {
		d.log.Warn("metrics flush failed", "err", err)
	}
	d.cur = map[string]*bucket{}
}

// writeMinute upserts the current buckets, merging with any row already
// there (e.g. written before a restart within the same minute).
func (d *Downsampler) writeMinute(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO metrics_1m (metric, ts, avg, min, max, n) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (metric, ts) DO UPDATE SET
			avg = (avg * n + excluded.avg * excluded.n) / (n + excluded.n),
			min = MIN(min, excluded.min),
			max = MAX(max, excluded.max),
			n   = n + excluded.n`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	ts := d.minute.Unix()
	for metric, b := range d.cur {
		if _, err := stmt.ExecContext(ctx, metric, ts, b.sum/float64(b.n), b.min, b.max, b.n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// hourly rolls up the two hours before h (the one just finished, and the one
// before in case a restart left it partial) and applies retention.
func (d *Downsampler) hourly(ctx context.Context, h time.Time) {
	for _, start := range []time.Time{h.Add(-2 * time.Hour), h.Add(-time.Hour)} {
		if err := d.rollup(ctx, start); err != nil {
			d.log.Warn("metrics rollup failed", "hour", start, "err", err)
		}
	}
	if err := d.prune(ctx); err != nil {
		d.log.Warn("metrics retention failed", "err", err)
	}
	d.hour = h
}

// rollup rebuilds the hourly rows for [start, start+1h) from metrics_1m with
// a count-weighted average. Idempotent.
func (d *Downsampler) rollup(ctx context.Context, start time.Time) error {
	from, to := start.Unix(), start.Add(time.Hour).Unix()
	_, err := d.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_1h (metric, ts, avg, min, max, n)
		SELECT metric, ?, SUM(avg * n) / SUM(n), MIN(min), MAX(max), SUM(n)
		FROM metrics_1m WHERE ts >= ? AND ts < ?
		GROUP BY metric`, from, from, to)
	return err
}

func (d *Downsampler) prune(ctx context.Context) error {
	now := d.now()
	if _, err := d.db.ExecContext(ctx, `DELETE FROM metrics_1m WHERE ts < ?`, now.Add(-Retention1m).Unix()); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM metrics_1h WHERE ts < ?`, now.Add(-Retention1h).Unix())
	return err
}

// catchUp rolls up every complete hour since the newest hourly row (at most
// the 1-minute retention window back), then prunes.
func (d *Downsampler) catchUp(ctx context.Context) error {
	now := d.now()
	h := now.Truncate(time.Hour)
	// Start at the newest hourly row, but never before the oldest minute
	// row (nothing to roll up) or the 1-minute retention window.
	var last, first int64
	if err := d.db.QueryRowContext(ctx, `SELECT
		(SELECT COALESCE(MAX(ts), 0) FROM metrics_1h),
		(SELECT COALESCE(MIN(ts), 0) FROM metrics_1m)`).Scan(&last, &first); err != nil {
		return fmt.Errorf("store: metrics catch-up: %w", err)
	}
	start := time.Unix(max(last, first), 0)
	if first == 0 {
		start = h // no minute data at all
	}
	if oldest := h.Add(-Retention1m); start.Before(oldest) {
		start = oldest
	}
	for t := start.Truncate(time.Hour); t.Before(h); t = t.Add(time.Hour) {
		if err := d.rollup(ctx, t); err != nil {
			return fmt.Errorf("store: metrics catch-up: %w", err)
		}
	}
	if err := d.prune(ctx); err != nil {
		return fmt.Errorf("store: metrics retention: %w", err)
	}
	d.hour = h
	return nil
}

// Series returns the buckets of metric in [from, to) at resolution res,
// oldest first.
func (db *DB) Series(ctx context.Context, res Resolution, metric string, from, to time.Time) ([]Agg, error) {
	table, err := res.table()
	if err != nil {
		return nil, err
	}
	// table is one of two constants, never user input.
	rows, err := db.QueryContext(ctx, `SELECT ts, avg, min, max, n FROM `+table+ // #nosec G202 -- table name from a fixed switch
		` WHERE metric = ? AND ts >= ? AND ts < ? ORDER BY ts`, metric, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: series: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agg
	for rows.Next() {
		var a Agg
		var ts int64
		if err := rows.Scan(&ts, &a.Avg, &a.Min, &a.Max, &a.N); err != nil {
			return nil, fmt.Errorf("store: series: %w", err)
		}
		a.Time = time.Unix(ts, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}
