package forecast

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	// Metric is the alert catalog's id.
	Metric = "disk.forecast_full"
	// usedPrefix and healthPrefix select the history (collect.Sample.Points).
	usedPrefix   = "disk.used:"
	healthPrefix = "battery.health:"
	// The history moves by the hour: nothing is gained by looking sooner.
	every      = time.Hour
	firstCheck = time.Minute
	// A forecast that fired stays firing until it moves past this, so one
	// around Soon does not notify every hour.
	clearDays = 10.0
)

// Mount is a mount's size now.
type Mount struct {
	Mount string
	Total uint64
	Avail uint64
}

// History is the subset of store.DB used here.
type History interface {
	History(ctx context.Context, res store.Resolution, sel []string, from, to time.Time, step time.Duration) (store.History, error)
}

// Config configures a Monitor.
type Config struct {
	DB History
	// Mounts lists the mounts now; empty until the first sample.
	Mounts func() []Mount
	// Observe receives each mount's state: 1 fills soon, 0 not.
	Observe func(ctx context.Context, now time.Time, values map[string]float64)
	Now     func() time.Time
	Log     *slog.Logger
}

// MountForecast is one mount in the report.
type MountForecast struct {
	Mount string `json:"mount"`
	DiskForecast
}

// Report is GET /api/v1/forecasts: only forecasts the data supports.
type Report struct {
	Disks     []MountForecast        `json:"disks"`
	Batteries map[string]BatteryWear `json:"batteries"`
}

// Monitor keeps the last report.
type Monitor struct {
	cfg Config

	mu     sync.Mutex
	report Report
	at     time.Time
	firing map[string]bool
}

// New returns a Monitor.
func New(cfg Config) *Monitor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Monitor{cfg: cfg, firing: map[string]bool{}, report: Report{Disks: []MountForecast{}, Batteries: map[string]BatteryWear{}}}
}

// series reads the metric's points from a History column.
func series(h store.History, metric string) []Point {
	var out []Point
	for i, v := range h.Avg[metric] {
		if v != nil {
			out = append(out, Point{At: time.UnixMilli(h.T[i]), V: *v})
		}
	}
	return out
}

// compute builds a report from the history.
func (m *Monitor) compute(ctx context.Context, now time.Time) (Report, error) {
	r := Report{Disks: []MountForecast{}, Batteries: map[string]BatteryWear{}}
	used, err := m.cfg.DB.History(ctx, store.Res1h, []string{usedPrefix}, now.Add(-30*day), now, time.Hour)
	if err != nil {
		return r, err
	}
	for _, d := range m.cfg.Mounts() {
		if f, ok := Disk(series(used, usedPrefix+d.Mount), float64(d.Total), float64(d.Avail), now); ok {
			r.Disks = append(r.Disks, MountForecast{Mount: d.Mount, DiskForecast: f})
		}
	}
	sort.Slice(r.Disks, func(i, j int) bool { return r.Disks[i].Mount < r.Disks[j].Mount })
	health, err := m.cfg.DB.History(ctx, store.Res1h, []string{healthPrefix}, now.Add(-batteryHistoryD*day), now, day)
	if err != nil {
		return r, err
	}
	for metric := range health.Avg {
		r.Batteries[strings.TrimPrefix(metric, healthPrefix)] = Battery(series(health, metric))
	}
	return r, nil
}

// refresh replaces the report and feeds the alert.
func (m *Monitor) refresh(ctx context.Context) {
	now := m.cfg.Now()
	r, err := m.compute(ctx, now)
	if err != nil {
		m.cfg.Log.Warn("forecast failed", "err", err)
		return
	}
	mounts := m.cfg.Mounts()
	values := make(map[string]float64, len(mounts))
	soon := make(map[string]float64, len(r.Disks))
	for _, d := range r.Disks {
		soon[d.Mount] = d.Days
	}
	m.mu.Lock()
	for _, d := range mounts {
		days, known := soon[d.Mount]
		on := known && (days <= Soon || (m.firing[d.Mount] && days <= clearDays))
		m.firing[d.Mount] = on
		values[d.Mount] = 0
		if on {
			values[d.Mount] = 1
		}
	}
	m.report, m.at = r, now
	m.mu.Unlock()
	// No mounts yet means no sample yet, not that every disk is fine.
	if len(mounts) > 0 && m.cfg.Observe != nil {
		m.cfg.Observe(ctx, now, values)
	}
}

// Run refreshes hourly until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	wait := firstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		m.refresh(ctx)
	}
}

// Handle serves GET /api/v1/forecasts.
func (m *Monitor) Handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	stale := m.at.IsZero()
	m.mu.Unlock()
	if stale {
		m.refresh(r.Context())
	}
	m.mu.Lock()
	report := m.report
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(report)
}
