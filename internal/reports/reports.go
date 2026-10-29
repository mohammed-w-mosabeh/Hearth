// Package reports builds machine and item reports from Hearth's history
// (SPEC 16) and renders them as self-contained HTML or Markdown.
package reports

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Store is the history a report reads (store.DB in production).
type Store interface {
	History(ctx context.Context, res store.Resolution, sel []string, from, to time.Time, step time.Duration) (store.History, error)
	TopItems(ctx context.Context, from, to time.Time, limit int) ([]store.ItemTotal, error)
	ItemHistory(ctx context.Context, item string, from, to time.Time) ([]store.ItemUsage, error)
	AlertEventsBetween(ctx context.Context, from, to time.Time, limit int) ([]store.AlertEvent, error)
	AuditBetween(ctx context.Context, from, to time.Time, limit int) ([]store.AuditRow, error)
}

// Limits on what one report lists.
const (
	maxTop     = 15
	maxAlerts  = 200
	maxChanges = 200
	trendSize  = 48 // points in a report's charts
)

// Periods a report can cover, ending now.
var Periods = map[string]time.Duration{
	"day":   24 * time.Hour,
	"week":  7 * 24 * time.Hour,
	"month": 30 * 24 * time.Hour,
}

// ErrPeriod is returned for a period not in Periods.
var ErrPeriod = errors.New("period must be day, week or month")

// Disk is one mounted filesystem.
type Disk struct {
	Mount string  `json:"mount"`
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	Pct   float64 `json:"pct"`
	// Growth is the change in used bytes over the period, if recorded.
	Growth *float64 `json:"growth,omitempty"`
}

// Now is the machine's current state, which history does not hold.
type Now struct {
	Host      string
	Disks     []Disk
	Batteries []power.Battery
}

// Config configures a Builder.
type Config struct {
	Store Store
	// Now returns the current state (hostname, totals, disks, batteries).
	Now func(ctx context.Context) Now
	// Clock is replaced in tests.
	Clock func() time.Time
}

// Builder builds reports.
type Builder struct{ cfg Config }

// New returns a Builder.
func New(cfg Config) *Builder {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Now == nil {
		cfg.Now = func(context.Context) Now { return Now{} }
	}
	return &Builder{cfg: cfg}
}

// Stat summarises one metric over a period. Unit says how to show it:
// "%", "bytes", "bytes/s", "°C" or "".
type Stat struct {
	Metric string  `json:"metric"`
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Avg    float64 `json:"avg"`
	Max    float64 `json:"max"`
	// Total is the bytes moved over the period, for throughput metrics.
	Total *float64 `json:"total,omitempty"`
}

// Trend is a metric over the period in at most trendSize points; T is
// unix milliseconds and V nil where nothing was recorded.
type Trend struct {
	Metric string     `json:"metric"`
	Label  string     `json:"label"`
	Unit   string     `json:"unit"`
	Peak   float64    `json:"peak"` // the largest point
	Max    float64    `json:"max"`  // the scale: 100 for %, else Peak
	T      []int64    `json:"t"`
	V      []*float64 `json:"v"`
}

// Machine is the machine report.
type Machine struct {
	Host      string             `json:"host"`
	Period    string             `json:"period"`
	From      time.Time          `json:"from"`
	To        time.Time          `json:"to"`
	Generated time.Time          `json:"generated"`
	Stats     []Stat             `json:"stats"`
	Trends    []Trend            `json:"trends"`
	Top       []store.ItemTotal  `json:"top"`
	Alerts    []store.AlertEvent `json:"alerts"`
	Changes   []store.AuditRow   `json:"changes"`
	Disks     []Disk             `json:"disks"`
	Batteries []power.Battery    `json:"batteries"`
	// Covered is the share of the period with recorded metrics (hearthd
	// was not running, or the machine was off, for the rest).
	Covered float64 `json:"covered"`
}

// Item is the report on one app, service or container.
type Item struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	Host      string             `json:"host"`
	Period    string             `json:"period"`
	From      time.Time          `json:"from"`
	To        time.Time          `json:"to"`
	Generated time.Time          `json:"generated"`
	Total     *store.ItemTotal   `json:"total"` // nil when never active in the period
	Hours     []store.ItemUsage  `json:"hours"`
	Trends    []Trend            `json:"trends"`
	PeakCPU   *store.ItemUsage   `json:"peak_cpu,omitempty"`
	PeakMem   *store.ItemUsage   `json:"peak_mem,omitempty"`
	Events    []store.AlertEvent `json:"events"`  // failures and unexpected exits
	Changes   []store.AuditRow   `json:"changes"` // actions on the item
}

// metricDef is a metric the machine report summarises.
type metricDef struct {
	sel, label, unit string
	throughput       bool // bytes/s: also show the bytes moved
	trend            bool
}

var machineMetrics = []metricDef{
	{sel: "cpu.usage", label: "CPU usage", unit: "%", trend: true},
	{sel: "load1", label: "Load (1 min)"},
	{sel: "mem.used", label: "Memory used", unit: "bytes", trend: true},
	{sel: "swap.used", label: "Swap used", unit: "bytes"},
	{sel: "cpu.temp", label: "CPU temperature", unit: "°C"},
	{sel: "gpu.util:", label: "GPU usage", unit: "%"},
	{sel: "gpu.temp:", label: "GPU temperature", unit: "°C"},
	{sel: "net.rx:", label: "Network received", unit: "bytes/s", throughput: true},
	{sel: "net.tx:", label: "Network sent", unit: "bytes/s", throughput: true},
	{sel: "disk.read:", label: "Disk read", unit: "bytes/s", throughput: true},
	{sel: "disk.write:", label: "Disk written", unit: "bytes/s", throughput: true},
}

// window returns the period's bounds and the resolution to read: minutes
// for a day (the hourly rollup lags up to an hour), hours otherwise.
func (b *Builder) window(period string) (from, to time.Time, res store.Resolution, step time.Duration, err error) {
	span, ok := Periods[period]
	if !ok {
		return from, to, res, step, ErrPeriod
	}
	to = b.cfg.Clock()
	from = to.Add(-span)
	if span <= 24*time.Hour {
		return from, to, store.Res1m, time.Minute, nil
	}
	return from, to, store.Res1h, time.Hour, nil
}

// Machine builds the machine report for period.
func (b *Builder) Machine(ctx context.Context, period string) (Machine, error) {
	from, to, res, step, err := b.window(period)
	if err != nil {
		return Machine{}, err
	}
	now := b.cfg.Now(ctx)
	m := Machine{Host: now.Host, Period: period, From: from, To: to, Generated: b.cfg.Clock(),
		Stats: []Stat{}, Trends: []Trend{}, Disks: now.Disks, Batteries: now.Batteries}
	if m.Disks == nil {
		m.Disks = []Disk{}
	}
	if m.Batteries == nil {
		m.Batteries = []power.Battery{}
	}

	sel := []string{"disk.used:"}
	for _, d := range machineMetrics {
		sel = append(sel, d.sel)
	}
	h, err := b.cfg.Store.History(ctx, res, sel, from, to, step)
	if err != nil {
		return Machine{}, fmt.Errorf("reports: %w", err)
	}
	if cpu, ok := h.Avg["cpu.usage"]; ok {
		m.Covered = min(1, float64(countSet(cpu))*step.Seconds()/to.Sub(from).Seconds())
	}
	for _, d := range machineMetrics {
		metrics := matching(h, d.sel)
		for _, name := range metrics {
			st, ok := summarise(h.Avg[name], h.Max[name], step)
			if !ok {
				continue
			}
			st.Metric, st.Unit, st.Label = name, d.unit, d.label
			if inst := instance(name, d.sel); inst != "" {
				st.Label += " (" + inst + ")"
			}
			if !d.throughput {
				st.Total = nil
			}
			m.Stats = append(m.Stats, st)
			if d.trend {
				m.Trends = append(m.Trends, trend(name, d.label, d.unit, h.T, h.Avg[name], from, to, step))
			}
		}
	}
	// Disk growth: the last recorded use minus the first.
	for i, d := range m.Disks {
		if g, ok := growth(h.Avg["disk.used:"+d.Mount]); ok {
			m.Disks[i].Growth = &g
		}
	}

	if m.Top, err = b.cfg.Store.TopItems(ctx, from, to, maxTop); err != nil {
		return Machine{}, fmt.Errorf("reports: %w", err)
	}
	if m.Alerts, err = b.cfg.Store.AlertEventsBetween(ctx, from, to, maxAlerts); err != nil {
		return Machine{}, fmt.Errorf("reports: %w", err)
	}
	if m.Changes, err = b.cfg.Store.AuditBetween(ctx, from, to, maxChanges); err != nil {
		return Machine{}, fmt.Errorf("reports: %w", err)
	}
	return m, nil
}

// Item builds the report on item (a process group ID such as
// "unit:cups.service") for period.
func (b *Builder) Item(ctx context.Context, id, period string) (Item, error) {
	from, to, _, _, err := b.window(period)
	if err != nil {
		return Item{}, err
	}
	kind, _, _ := strings.Cut(id, ":")
	it := Item{ID: id, Kind: kind, Name: id, Host: b.cfg.Now(ctx).Host, Period: period, From: from, To: to,
		Generated: b.cfg.Clock(), Trends: []Trend{}, Events: []store.AlertEvent{}, Changes: []store.AuditRow{}}
	if it.Hours, err = b.cfg.Store.ItemHistory(ctx, id, from, to); err != nil {
		return Item{}, fmt.Errorf("reports: %w", err)
	}
	// The usage store keeps hours, so even a day is read hour by hour; the
	// first hour may start before the period.
	if len(it.Hours) > 0 {
		last := it.Hours[len(it.Hours)-1]
		it.Kind, it.Name = last.Kind, last.Name
		t := itemTotal(it.Hours)
		it.Total = &t
		pc, pm := it.Hours[0], it.Hours[0]
		for _, h := range it.Hours {
			if h.CPUMax > pc.CPUMax {
				pc = h
			}
			if h.MemMax > pm.MemMax {
				pm = h
			}
		}
		it.PeakCPU, it.PeakMem = &pc, &pm
		ts := make([]int64, len(it.Hours))
		cpu := make([]*float64, len(it.Hours))
		mem := make([]*float64, len(it.Hours))
		for i, h := range it.Hours {
			ts[i] = h.Time.UnixMilli()
			cpu[i], mem[i] = &it.Hours[i].CPUAvg, &it.Hours[i].MemAvg
		}
		it.Trends = append(it.Trends,
			trend("cpu", "CPU usage", "%", ts, cpu, from, to, time.Hour),
			trend("mem", "Memory used", "bytes", ts, mem, from, to, time.Hour))
	}

	events, err := b.cfg.Store.AlertEventsBetween(ctx, from, to, maxAlerts*5)
	if err != nil {
		return Item{}, fmt.Errorf("reports: %w", err)
	}
	for _, e := range events {
		if itemEvent(id, it.Name, e) && len(it.Events) < maxAlerts {
			it.Events = append(it.Events, e)
		}
	}
	names := itemTargets(id, it.Name)
	changes, err := b.cfg.Store.AuditBetween(ctx, from, to, maxChanges*5)
	if err != nil {
		return Item{}, fmt.Errorf("reports: %w", err)
	}
	for _, c := range changes {
		if names[c.Target] && len(it.Changes) < maxChanges {
			it.Changes = append(it.Changes, c)
		}
	}
	return it, nil
}

// itemEvent reports whether alert e is about item id: a unit's failure
// ("unit.failed" on "system/<unit>" or "user/<unit>"), or a container's
// unexpected exit ("container.exited" on its name).
func itemEvent(id, name string, e store.AlertEvent) bool {
	kind, rest, _ := strings.Cut(id, ":")
	switch kind {
	case "unit":
		return e.Metric == "unit.failed" && e.Instance == "system/"+rest
	case "user-unit":
		return e.Metric == "unit.failed" && e.Instance == "user/"+rest
	case "container":
		return e.Metric == "container.exited" && (e.Instance == rest || (e.Instance == name && name != id))
	}
	return false
}

// itemTargets are the audit log targets of actions on an item: units are
// "<scope>/<unit>", containers their ID or name, apps their ID.
func itemTargets(id, name string) map[string]bool {
	out := map[string]bool{id: true}
	kind, rest, _ := strings.Cut(id, ":")
	switch kind {
	case "unit":
		out["system/"+rest] = true
	case "user-unit":
		out["user/"+rest] = true
	case "container":
		out[rest] = true
		if name != "" && name != id {
			out[name] = true
		}
	case "app":
		out[rest] = true
	}
	return out
}

func itemTotal(hours []store.ItemUsage) store.ItemTotal {
	t := store.ItemTotal{Item: hours[0].Item, Kind: hours[len(hours)-1].Kind, Name: hours[len(hours)-1].Name, Hours: len(hours)}
	var n int
	var cpu, mem float64
	for _, h := range hours {
		n += h.N
		cpu += h.CPUAvg * float64(h.N)
		mem += h.MemAvg * float64(h.N)
		t.CPUMax = max(t.CPUMax, h.CPUMax)
		t.MemMax = max(t.MemMax, h.MemMax)
	}
	t.CPUAvg, t.MemAvg = cpu/float64(n), mem/float64(n)
	t.Busy = cpu / 60 / 100 // samples are a minute apart (as store.TopItems)
	return t
}

// matching lists the metrics of h selected by sel, sorted.
func matching(h store.History, sel string) []string {
	var out []string
	for name := range h.Avg {
		if store.MatchMetric(name, []string{sel}) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// instance is what follows a prefix selector ("net.rx:wlan0" -> "wlan0").
func instance(name, sel string) string {
	if !store.IsPrefix(sel) {
		return ""
	}
	return strings.TrimPrefix(name, sel)
}

// summarise averages the recorded buckets (each covers step, so this is
// the time average) and adds up throughput over them.
func summarise(avg, mx []*float64, step time.Duration) (Stat, bool) {
	var st Stat
	var sum float64
	n := 0
	for i, v := range avg {
		if v == nil {
			continue
		}
		sum += *v
		n++
		if mx[i] != nil {
			st.Max = max(st.Max, *mx[i])
		}
	}
	if n == 0 {
		return st, false
	}
	st.Avg = sum / float64(n)
	total := sum * step.Seconds()
	st.Total = &total
	return st, true
}

func countSet(v []*float64) int {
	n := 0
	for _, x := range v {
		if x != nil {
			n++
		}
	}
	return n
}

func growth(v []*float64) (float64, bool) {
	var first, last *float64
	for _, x := range v {
		if x == nil {
			continue
		}
		if first == nil {
			first = x
		}
		last = x
	}
	if first == nil {
		return 0, false
	}
	return *last - *first, true
}

// trend averages (t, v) into size equal slices of [from, to); data at a
// resolution of res needs slices at least that long to have no gaps.
func trend(metric, label, unit string, t []int64, v []*float64, from, to time.Time, res time.Duration) Trend {
	size := int(min(int64(trendSize), max(int64(to.Sub(from)/res), 1)))
	tr := Trend{Metric: metric, Label: label, Unit: unit, T: make([]int64, size), V: make([]*float64, size)}
	start, span := from.UnixMilli(), to.UnixMilli()-from.UnixMilli()
	sum := make([]float64, size)
	n := make([]int, size)
	for i, ts := range t {
		if i >= len(v) || v[i] == nil || span <= 0 {
			continue
		}
		k := int((ts - start) * int64(size) / span)
		if k < 0 || k >= size {
			continue
		}
		sum[k] += *v[i]
		n[k]++
	}
	for k := range size {
		tr.T[k] = start + int64(k)*span/int64(size)
		if n[k] > 0 {
			a := sum[k] / float64(n[k])
			tr.V[k] = &a
			tr.Peak = max(tr.Peak, a)
		}
	}
	tr.Max = tr.Peak
	if unit == "%" {
		tr.Max = 100
	}
	if tr.Max == 0 || math.IsNaN(tr.Max) {
		tr.Max = 1
	}
	return tr
}
