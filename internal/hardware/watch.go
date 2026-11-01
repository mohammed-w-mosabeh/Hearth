package hardware

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// Metric is the alert catalog's id.
const Metric = "usb.new_device"

const watchEvery = time.Minute

// Watcher raises "usb.new_device" once for a device never seen here. The
// first reading is the baseline: what is already plugged in is not news.
type Watcher struct {
	Paths   Paths
	Load    func(ctx context.Context) ([]string, bool, error)
	Save    func(ctx context.Context, known []string) error
	Observe func(ctx context.Context, now time.Time, v map[string]float64)
	Now     func() time.Time
	Log     *slog.Logger
}

// label names a device in the alert; the ID tells models apart.
func label(d Device) string {
	name := d.Name
	if name == "" {
		name = d.Vendor
	}
	if name == "" {
		return d.ID
	}
	return clean(name + " (" + d.ID + ")")
}

// check compares devices with known and returns what is new, the new
// known list, and whether it changed.
func check(devices []Device, known []string, first bool) (news map[string]float64, all []string, changed bool) {
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[k] = true
	}
	news = map[string]float64{}
	for _, d := range devices {
		if set[d.ID] {
			continue
		}
		set[d.ID], changed = true, true
		if !first {
			news[label(d)] = 1
		}
	}
	all = make([]string, 0, len(set))
	for k := range set {
		all = append(all, k)
	}
	sort.Strings(all)
	if len(all) > maxDevices*4 { // bounded: oldest order is not kept, any is fine
		all = all[:maxDevices*4]
	}
	return news, all, changed
}

// Run watches until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	p := w.Paths.withDefaults()
	known, seen, err := w.Load(ctx)
	if err != nil {
		w.Log.Warn("hardware: known devices", "err", err)
		return
	}
	first := !seen
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for {
		news, all, changed := check(readUSB(p), known, first)
		first = false
		known = all
		if changed {
			if err := w.Save(ctx, all); err != nil && ctx.Err() == nil {
				w.Log.Warn("hardware: known devices", "err", err)
			}
		}
		// Every reading, so last time's incident ends: one notification.
		w.Observe(ctx, w.Now(), news)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
