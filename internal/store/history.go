package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A metric selector is a metric name, or a prefix ending in ":" or "."
// that selects every metric under it ("net.rx:" for all interfaces,
// "cpu.core." for all cores).

// IsPrefix reports whether selector sel is a prefix.
func IsPrefix(sel string) bool {
	return strings.HasSuffix(sel, ":") || strings.HasSuffix(sel, ".")
}

// MatchMetric reports whether metric is selected by any of sel.
func MatchMetric(metric string, sel []string) bool {
	for _, s := range sel {
		if metric == s || (IsPrefix(s) && strings.HasPrefix(metric, s)) {
			return true
		}
	}
	return false
}

// History is metric history as columns: T holds unix milliseconds of each
// bucket start; Avg and Max hold one value per T per metric, nil where the
// metric has no data in that bucket.
type History struct {
	T   []int64               `json:"t"`
	Avg map[string][]*float64 `json:"series"`
	Max map[string][]*float64 `json:"max"`
}

// History returns the metrics matched by sel in [from, to) at resolution
// res, merged into buckets of step (a multiple of the resolution): the
// average is weighted by sample count, the max is the max.
func (db *DB) History(ctx context.Context, res Resolution, sel []string, from, to time.Time, step time.Duration) (History, error) {
	out := History{T: []int64{}, Avg: map[string][]*float64{}, Max: map[string][]*float64{}}
	table, err := res.table()
	if err != nil {
		return out, err
	}
	stepS := int64(step / time.Second)
	if stepS <= 0 || len(sel) == 0 {
		return out, fmt.Errorf("store: history: bad step or empty selection")
	}
	var conds []string
	args := []any{stepS, stepS}
	for _, s := range sel {
		if IsPrefix(s) {
			conds = append(conds, "substr(metric, 1, ?) = ?")
			args = append(args, len(s), s)
		} else {
			conds = append(conds, "metric = ?")
			args = append(args, s)
		}
	}
	args = append(args, from.Unix(), to.Unix())
	// table is one of two constants and conds are fixed fragments; every
	// value is a bound parameter.
	q := `SELECT metric, (ts / ?) * ? AS b, SUM(avg * n) / SUM(n), MAX(max) FROM ` + table + // #nosec G202 -- table name from a fixed switch, conditions are constants
		` WHERE (` + strings.Join(conds, " OR ") + `) AND ts >= ? AND ts < ? GROUP BY metric, b`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return out, fmt.Errorf("store: history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type cell struct{ avg, max float64 }
	cells := map[string]map[int64]cell{}
	buckets := map[int64]bool{}
	for rows.Next() {
		var m string
		var b int64
		var c cell
		if err := rows.Scan(&m, &b, &c.avg, &c.max); err != nil {
			return out, fmt.Errorf("store: history: %w", err)
		}
		if cells[m] == nil {
			cells[m] = map[int64]cell{}
		}
		cells[m][b] = c
		buckets[b] = true
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store: history: %w", err)
	}

	ts := make([]int64, 0, len(buckets))
	for b := range buckets {
		ts = append(ts, b)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	for _, b := range ts {
		out.T = append(out.T, b*1000)
	}
	for m, byB := range cells {
		avg := make([]*float64, len(ts))
		mx := make([]*float64, len(ts))
		for i, b := range ts {
			if c, ok := byB[b]; ok {
				avg[i], mx[i] = &c.avg, &c.max
			}
		}
		out.Avg[m], out.Max[m] = avg, mx
	}
	return out, nil
}
