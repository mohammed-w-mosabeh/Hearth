// Package insight answers two questions from what Hearth already measures
// (SPEC 18, ROADMAP 4.8): why the machine is hot or loud right now, and
// roughly how much energy each app uses. Nothing here needs a privilege:
// RAPL's energy counters are root's, so energy is estimated from CPU time.
package insight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Causes, most specific first; the UI words them.
const (
	CauseApp     = "app"     // one item is busy
	CauseGPU     = "gpu"     // a graphics device is busy
	CauseSystem  = "system"  // busy, but no single item stands out
	CauseCooling = "cooling" // load ended a moment ago, the fans catch up
	CauseHotIdle = "hot-idle"
	CauseQuiet   = "quiet"
)

// Thresholds.
const (
	busyItem   = 10.0 // % of the machine
	busyGPU    = 50.0 // % of the device
	busyCPU    = 40.0 // % of the machine
	hotIdleC   = 75.0
	coolingC   = 10.0 // fall in temperature that means it is cooling down
	recentLoad = 5 * time.Minute
	maxCauses  = 3
	window     = 2 * time.Second
	topItems   = 8
)

// Cause is one reason, with what it is about.
type Cause struct {
	Code    string  `json:"code"`
	Subject string  `json:"subject,omitempty"`
	Value   float64 `json:"value,omitempty"` // % busy
}

// GPU is a device's state now.
type GPU struct {
	Name   string   `json:"name"`
	Util   *float64 `json:"util,omitempty"`
	TempC  *float64 `json:"temp_c,omitempty"`
	PowerW *float64 `json:"power_w,omitempty"`
}

// Diagnosis is the answer to "why is my fan loud?".
type Diagnosis struct {
	Time   time.Time `json:"time"`
	Causes []Cause   `json:"causes"`
	CPU    float64   `json:"cpu"`
	TempC  *float64  `json:"temp_c,omitempty"`
	// Fans is empty when the machine reports no fan speed.
	Fans []collect.Fan     `json:"fans"`
	GPUs []GPU             `json:"gpus"`
	Top  []collect.ItemNow `json:"top"`
}

// Diagnose reads s, the samples before it and what is busy now.
func Diagnose(s collect.Sample, recent []collect.Sample, top []collect.ItemNow) Diagnosis {
	d := Diagnosis{Time: s.Time, CPU: s.CPU.Usage, TempC: s.CPUTempC, Fans: s.Fans, GPUs: []GPU{}, Top: top, Causes: []Cause{}}
	if d.Fans == nil {
		d.Fans = []collect.Fan{}
	}
	if d.Top == nil {
		d.Top = []collect.ItemNow{}
	}
	var sum float64
	for _, it := range top {
		sum += it.CPU
		if it.CPU >= busyItem && len(d.Causes) < maxCauses {
			d.Causes = append(d.Causes, Cause{Code: CauseApp, Subject: it.Name, Value: it.CPU})
		}
	}
	for _, g := range s.GPUs {
		d.GPUs = append(d.GPUs, GPU{Name: g.Name, Util: g.UtilPct, TempC: g.TempC, PowerW: g.PowerW})
		if g.UtilPct != nil && *g.UtilPct >= busyGPU {
			d.Causes = append(d.Causes, Cause{Code: CauseGPU, Subject: g.Name, Value: *g.UtilPct})
		}
	}
	if len(d.Causes) > 0 {
		return d
	}
	if s.CPU.Usage >= busyCPU {
		d.Causes = append(d.Causes, Cause{Code: CauseSystem, Value: s.CPU.Usage})
		return d
	}
	for _, r := range recent {
		if s.Time.Sub(r.Time) > recentLoad {
			continue
		}
		wasBusy := !r.Partial && r.CPU.Usage >= busyCPU
		// One busy core is little of the machine but plenty of heat.
		wasHotter := r.CPUTempC != nil && s.CPUTempC != nil && *r.CPUTempC-*s.CPUTempC >= coolingC
		if wasBusy || wasHotter {
			d.Causes = append(d.Causes, Cause{Code: CauseCooling})
			return d
		}
	}
	if s.CPUTempC != nil && *s.CPUTempC >= hotIdleC {
		d.Causes = append(d.Causes, Cause{Code: CauseHotIdle, Value: *s.CPUTempC})
		return d
	}
	d.Causes = append(d.Causes, Cause{Code: CauseQuiet})
	return d
}

// Energy estimates.

// nominalWatts is what a full CPU is assumed to add when this machine has
// taught nothing better; a typical laptop's sustained load.
const nominalWatts = 30.0

// Fit limits: too few hours or a nonsense slope falls back to nominal.
const (
	minFitHours = 8
	minWatts    = 5.0
	maxWatts    = 300.0
)

// fit learns watts at 100% CPU above idle from hours on battery, as the
// slope of rate over CPU. Hours are pairs of CPU % and watts.
func fit(cpu, watts []float64) (full float64, ok bool) {
	n := float64(len(cpu))
	if len(cpu) < minFitHours {
		return 0, false
	}
	var sx, sy, sxx, sxy float64
	for i := range cpu {
		sx += cpu[i]
		sy += watts[i]
		sxx += cpu[i] * cpu[i]
		sxy += cpu[i] * watts[i]
	}
	den := n*sxx - sx*sx
	if den <= 0 {
		return 0, false
	}
	full = (n*sxy - sx*sy) / den * 100
	return full, full >= minWatts && full <= maxWatts
}

// Store is what energy reads (store.DB in production).
type Store interface {
	TopItems(ctx context.Context, from, to time.Time, limit int) ([]store.ItemTotal, error)
	History(ctx context.Context, res store.Resolution, sel []string, from, to time.Time, step time.Duration) (store.History, error)
}

// EnergyItem is an item's estimate.
type EnergyItem struct {
	Item  string  `json:"item"`
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Wh    float64 `json:"wh"`
	Share float64 `json:"share"` // % of the items listed
	Hours int     `json:"hours"`
}

// Energy is the estimate for a period.
type Energy struct {
	Period string       `json:"period"`
	Items  []EnergyItem `json:"items"`
	Total  float64      `json:"total_wh"`
	// FullWatts is what a full CPU is taken to add; Fitted says it was
	// learned from FitHours hours on this machine's battery.
	FullWatts float64 `json:"full_watts"`
	Fitted    bool    `json:"fitted"`
	FitHours  int     `json:"fit_hours"`
}

const fitSpan = 90 * 24 * time.Hour

// calibrate pairs CPU with discharge power, hour by hour: an hour counts
// when the battery's charge fell into the next one.
func calibrate(h store.History) (cpu, watts []float64) {
	usage := h.Avg["cpu.usage"]
	for name, pct := range h.Avg {
		id, ok := cutBattery(name)
		if !ok {
			continue
		}
		rate := h.Avg["battery.rate:"+id]
		for i := 0; i+1 < len(pct) && i < len(rate) && i < len(usage); i++ {
			if pct[i] == nil || pct[i+1] == nil || rate[i] == nil || usage[i] == nil {
				continue
			}
			if *pct[i+1] < *pct[i] && *rate[i] > 0 {
				cpu = append(cpu, *usage[i])
				watts = append(watts, *rate[i])
			}
		}
	}
	return cpu, watts
}

func cutBattery(name string) (string, bool) {
	const p = "battery.pct:"
	if len(name) > len(p) && name[:len(p)] == p {
		return name[len(p):], true
	}
	return "", false
}

var periods = map[string]time.Duration{"day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour}

// Config configures an API.
type Config struct {
	Store Store
	// Samples returns the recent samples, oldest first. With nobody
	// watching they are 15 s apart, so they only tell about the past.
	Samples func() []collect.Sample
	// Probe reads the machine now; its CPU use is since the previous call.
	// Only one request calls it at a time.
	Probe func() (collect.Sample, error)
	// Busy measures what is busy now (collect.UsageRecorder.Now).
	Busy  func(ctx context.Context, window time.Duration, limit int) ([]collect.ItemNow, error)
	Clock func() time.Time
}

// API serves the routes.
type API struct {
	cfg Config
	mu  sync.Mutex // one measurement at a time: Probe keeps deltas
}

// New returns an API.
func New(cfg Config) *API {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &API{cfg: cfg}
}

// Energy estimates a period.
func (a *API) Energy(ctx context.Context, period string) (Energy, error) {
	span, ok := periods[period]
	if !ok {
		return Energy{}, errors.New("period must be day, week or month")
	}
	now := a.cfg.Clock()
	out := Energy{Period: period, Items: []EnergyItem{}, FullWatts: nominalWatts}
	h, err := a.cfg.Store.History(ctx, store.Res1h, []string{"cpu.usage", "battery.pct:", "battery.rate:"}, now.Add(-fitSpan), now, time.Hour)
	if err != nil {
		return out, fmt.Errorf("insight: %w", err)
	}
	cpu, watts := calibrate(h)
	if full, ok := fit(cpu, watts); ok {
		out.FullWatts, out.Fitted, out.FitHours = full, true, len(cpu)
	}
	top, err := a.cfg.Store.TopItems(ctx, now.Add(-span), now, 20)
	if err != nil {
		return out, fmt.Errorf("insight: %w", err)
	}
	for _, t := range top {
		wh := t.Busy * out.FullWatts
		out.Total += wh
		out.Items = append(out.Items, EnergyItem{Item: t.Item, Kind: t.Kind, Name: t.Name, Wh: wh, Hours: t.Hours})
	}
	if out.Total > 0 {
		for i := range out.Items {
			out.Items[i].Share = out.Items[i].Wh / out.Total * 100
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].Wh > out.Items[j].Wh })
	return out, nil
}

// Explain measures for a moment and diagnoses that moment, so the
// machine's state and what is busy describe the same seconds.
func (a *API) Explain(r *http.Request) (Diagnosis, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.cfg.Probe(); err != nil {
		return Diagnosis{}, err
	}
	top, err := a.cfg.Busy(r.Context(), window, topItems)
	if err != nil {
		return Diagnosis{}, err
	}
	s, err := a.cfg.Probe()
	if err != nil {
		return Diagnosis{}, err
	}
	return Diagnose(s, a.cfg.Samples(), top), nil
}

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Register adds the routes.
func (a *API) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/insight/fan", func(w http.ResponseWriter, r *http.Request) {
		d, err := a.Explain(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not measure what is busy"})
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("GET /api/v1/insight/energy", func(w http.ResponseWriter, r *http.Request) {
		e, err := a.Energy(r.Context(), r.URL.Query().Get("period"))
		if err != nil {
			status := http.StatusInternalServerError
			if _, ok := periods[r.URL.Query().Get("period")]; !ok {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, e)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
