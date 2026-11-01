// Package forecast turns the hourly metric history into statements about
// the future: when a disk fills up, how fast a battery wears. A forecast
// that the data does not support is not made at all: the fit has to cover
// enough time, explain most of the variation and come from steady growth,
// not from one big download.
package forecast

import (
	"math"
	"time"
)

const day = 24 * time.Hour

// Disk fits.
const (
	// Soon is how many days out a disk counts as filling soon.
	Soon = 7.0
	// horizonDays is the furthest forecast worth stating.
	horizonDays = 730
	minDiskR2   = 0.7
	// A fall of more than this share of the disk in one step is space
	// freed: only what came after it says anything about the future.
	freedShare = 0.005
	// Growth under this share of the disk over the fit is a flat disk.
	minGrowthShare = 0.002
	// No single step may be more than this share of the growth.
	maxStepShare = 0.5
	// Data older than this says nothing about now (the machine was off).
	maxAge = 2 * day
)

// windows are the fits tried, shortest first: recent behaviour wins.
var windows = []struct {
	days    int
	span    float64 // least days covered
	samples int
}{
	{7, 3, 48},
	{30, 10, 120},
}

// Battery fits.
const (
	minBatteryR2    = 0.5
	minBatterySpan  = 30.0 // days
	minBatteryDays  = 14
	daysPerYear     = 365.25
	maxLossPerYear  = 100.0
	batteryHistoryD = 365
)

// Point is one value at one time, oldest first in a series.
type Point struct {
	At time.Time
	V  float64
}

// Fit is a least-squares line.
type Fit struct {
	PerDay float64 // slope
	R2     float64
	Span   float64 // days covered
}

// line fits a line through pts; ok is false with fewer than two distinct
// times or values that do not stay finite.
func line(pts []Point) (Fit, bool) {
	if len(pts) < 2 {
		return Fit{}, false
	}
	// Days from the first point, which keeps the sums well conditioned.
	var sx, sy float64
	start := pts[0].At
	for _, p := range pts {
		sx += p.At.Sub(start).Hours() / 24
		sy += p.V
	}
	n := float64(len(pts))
	mx, my := sx/n, sy/n
	var sxx, sxy, syy float64
	for _, p := range pts {
		dx, dy := p.At.Sub(start).Hours()/24-mx, p.V-my
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	if sxx <= 0 || math.IsNaN(sxy+syy) || math.IsInf(sxy+syy, 0) {
		return Fit{}, false
	}
	f := Fit{PerDay: sxy / sxx, Span: pts[len(pts)-1].At.Sub(start).Hours() / 24}
	if syy > 0 {
		f.R2 = sxy * sxy / (sxx * syy)
	}
	return f, true
}

// sinceFreed drops everything up to the last time space was freed.
func sinceFreed(pts []Point, capacity float64) []Point {
	for i := len(pts) - 1; i > 0; i-- {
		if pts[i-1].V-pts[i].V > capacity*freedShare {
			return pts[i:]
		}
	}
	return pts
}

// after returns the points at or after t.
func after(pts []Point, t time.Time) []Point {
	for i, p := range pts {
		if !p.At.Before(t) {
			return pts[i:]
		}
	}
	return nil
}

// DiskForecast says when a disk fills.
type DiskForecast struct {
	Days       float64 `json:"days"`        // from now until nothing is available
	PerDay     float64 `json:"per_day"`     // bytes of growth a day
	WindowDays int     `json:"window_days"` // the fit used, 7 or 30
	R2         float64 `json:"r2"`
}

// steady reports whether pts rise evenly enough to extend.
func steady(pts []Point, f Fit, capacity float64) bool {
	growth := pts[len(pts)-1].V - pts[0].V
	if f.PerDay <= 0 || f.R2 < minDiskR2 || f.PerDay*f.Span < capacity*minGrowthShare || growth <= 0 {
		return false
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].V-pts[i-1].V > growth*maxStepShare {
			return false
		}
	}
	return true
}

// Disk forecasts one mount from its used bytes, oldest first. capacity and
// avail are its size and the bytes still available now.
func Disk(pts []Point, capacity, avail float64, now time.Time) (DiskForecast, bool) {
	if len(pts) == 0 || capacity <= 0 || avail < 0 || now.Sub(pts[len(pts)-1].At) > maxAge {
		return DiskForecast{}, false
	}
	for _, w := range windows {
		s := sinceFreed(after(pts, now.Add(-time.Duration(w.days)*day)), capacity)
		if len(s) < w.samples {
			continue
		}
		f, ok := line(s)
		if !ok || f.Span < w.span || !steady(s, f, capacity) {
			continue
		}
		days := avail / f.PerDay
		if days > horizonDays {
			continue
		}
		return DiskForecast{Days: days, PerDay: f.PerDay, WindowDays: w.days, R2: f.R2}, true
	}
	return DiskForecast{}, false
}

// BatteryWear is how a battery's capacity has moved.
type BatteryWear struct {
	// LossPerYear is percentage points of design capacity lost a year;
	// nil when the history does not support a figure.
	LossPerYear *float64  `json:"loss_per_year,omitempty"`
	SpanDays    float64   `json:"span_days"`
	T           []int64   `json:"t"`   // unix seconds, one point a day
	Pct         []float64 `json:"pct"` // full vs design capacity
}

// Battery summarises daily health figures, oldest first.
func Battery(pts []Point) BatteryWear {
	w := BatteryWear{T: make([]int64, len(pts)), Pct: make([]float64, len(pts))}
	for i, p := range pts {
		w.T[i], w.Pct[i] = p.At.Unix(), p.V
	}
	f, ok := line(pts)
	if !ok {
		return w
	}
	w.SpanDays = f.Span
	if loss := -f.PerDay * daysPerYear; len(pts) >= minBatteryDays && f.Span >= minBatterySpan &&
		f.R2 >= minBatteryR2 && loss > 0 && loss <= maxLossPerYear {
		w.LossPerYear = &loss
	}
	return w
}
