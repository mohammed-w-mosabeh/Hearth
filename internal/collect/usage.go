package collect

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// UsageStore keeps hourly per-item usage (store.DB in production).
type UsageStore interface {
	AddItemUsage(ctx context.Context, rows []store.ItemUsage) error
	PruneItemUsage(ctx context.Context, before time.Time) error
}

// UsageConfig configures the usage recorder.
type UsageConfig struct {
	CgroupRoot string // default /sys/fs/cgroup
	UID        int
	Store      UsageStore
	Log        *slog.Logger
	// Name, if set, names an item from its group ID (an app's .desktop
	// name, a container's name); otherwise the process group's name.
	Name func(id string) (string, bool)
	// Interval between samples; default 1 min.
	Interval time.Duration
}

// Thresholds for keeping an item's hour: anything quieter is not worth a
// row (ROADMAP 4.3: reports show top consumers and item history).
const (
	usageMinCPU   = 0.5      // % of the machine, peak in the hour
	usageMinMem   = 64 << 20 // bytes, peak in the hour
	usageMaxItems = 100      // rows per hour
	usageKeep     = 90 * 24 * time.Hour
)

// UsageRecorder samples the CPU and memory of every app, service and
// container from their cgroups (cpu.stat, memory.current: no /proc walk)
// and stores hourly aggregates for reports.
type UsageRecorder struct {
	cfg  UsageConfig
	ncpu float64

	prevCPU map[string]uint64 // cgroup -> usage_usec
	prevAt  time.Time
	hour    time.Time
	acc     map[string]*usageAcc
}

type usageAcc struct {
	kind, name     string
	cpuSum, cpuMax float64
	memSum, memMax float64
	n              int
}

// NewUsageRecorder returns a recorder; call Run.
func NewUsageRecorder(cfg UsageConfig) *UsageRecorder {
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &UsageRecorder{cfg: cfg, ncpu: float64(runtime.NumCPU()), prevCPU: map[string]uint64{}, acc: map[string]*usageAcc{}}
}

// Run samples until ctx ends, then stores the hour so far.
func (u *UsageRecorder) Run(ctx context.Context) {
	t := time.NewTicker(u.cfg.Interval)
	defer t.Stop()
	u.Sample(time.Now()) // baseline for CPU deltas
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			u.flush(fctx)
			cancel()
			return
		case now := <-t.C:
			if h := now.Truncate(time.Hour); !u.hour.IsZero() && !h.Equal(u.hour) {
				u.flush(ctx)
			}
			u.Sample(now)
		}
	}
}

// leaf is one unit cgroup and its group.
type leaf struct {
	rel      string
	id, kind string
	name     string
}

// leaves lists the populated unit cgroups of the system and of the
// user's own manager.
func (u *UsageRecorder) leaves() []leaf {
	var out []leaf
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(filepath.Join(u.cfg.CgroupRoot, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name, child := e.Name(), rel+"/"+e.Name()
			switch {
			case strings.HasSuffix(name, ".slice") && depth < 4:
				walk(child, depth+1)
			case strings.HasSuffix(name, ".service") || strings.HasSuffix(name, ".scope"):
				g := groupFor(child, "")
				if g.kind == KindSession || g.kind == KindKernel || !populated(filepath.Join(u.cfg.CgroupRoot, child)) {
					continue
				}
				out = append(out, leaf{rel: child, id: g.id, kind: g.kind, name: g.name})
			}
		}
	}
	walk("/system.slice", 1)
	us := u.userSlice()
	walk(us+"/user@"+strconv.Itoa(u.cfg.UID)+".service", 2)
	return out
}

func (u *UsageRecorder) userSlice() string {
	return "/user.slice/user-" + strconv.Itoa(u.cfg.UID) + ".slice"
}

// Sample reads every leaf once and adds it to the current hour.
func (u *UsageRecorder) Sample(now time.Time) {
	elapsed := now.Sub(u.prevAt).Seconds()
	first := u.prevAt.IsZero()
	cur := map[string]uint64{}
	type val struct {
		kind, name string
		cpu, mem   float64
	}
	items := map[string]*val{}
	for _, l := range u.leaves() {
		dir := filepath.Join(u.cfg.CgroupRoot, l.rel)
		usec, ok := cpuUsage(dir)
		if !ok {
			continue
		}
		cur[l.rel] = usec
		v := items[l.id]
		if v == nil {
			v = &val{kind: l.kind, name: l.name}
			if u.cfg.Name != nil {
				if n, ok := u.cfg.Name(l.id); ok && n != "" {
					v.name = n
				}
			}
			items[l.id] = v
		}
		if prev, ok := u.prevCPU[l.rel]; ok && !first && elapsed > 0 && usec >= prev {
			v.cpu += float64(usec-prev) / (elapsed * 1e6 * u.ncpu) * 100
		}
		if b, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil { // #nosec G304 -- under the cgroup root
			if m, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
				v.mem += float64(m)
			}
		}
	}
	u.prevCPU, u.prevAt = cur, now
	u.hour = now.Truncate(time.Hour)
	if first {
		return
	}
	for id, v := range items {
		a := u.acc[id]
		if a == nil {
			a = &usageAcc{kind: v.kind, name: v.name}
			u.acc[id] = a
		}
		a.cpuSum += v.cpu
		a.memSum += v.mem
		a.cpuMax = max(a.cpuMax, v.cpu)
		a.memMax = max(a.memMax, v.mem)
		a.n++
	}
}

// cpuUsage reads usage_usec from a cgroup's cpu.stat.
func cpuUsage(dir string) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "cpu.stat")) // #nosec G304 -- under the cgroup root
	if err != nil {
		return 0, false
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte("usage_usec ")); ok {
			n, err := strconv.ParseUint(string(bytes.TrimSpace(v)), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// rows turns the hour's totals into rows: active items only, the busiest
// usageMaxItems.
func (u *UsageRecorder) rows() []store.ItemUsage {
	var out []store.ItemUsage
	for id, a := range u.acc {
		if a.n == 0 || (a.cpuMax < usageMinCPU && a.memMax < usageMinMem) {
			continue
		}
		out = append(out, store.ItemUsage{Item: id, Kind: a.kind, Name: a.name, Time: u.hour,
			CPUAvg: a.cpuSum / float64(a.n), CPUMax: a.cpuMax, MemAvg: a.memSum / float64(a.n), MemMax: a.memMax, N: a.n})
	}
	// Busiest first: CPU, then memory as a tie-breaker scale (1 GiB ~ 1%).
	score := func(r store.ItemUsage) float64 { return r.CPUAvg + r.MemAvg/(1<<30) }
	sort.Slice(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	if len(out) > usageMaxItems {
		out = out[:usageMaxItems]
	}
	return out
}

func (u *UsageRecorder) flush(ctx context.Context) {
	rows := u.rows()
	u.acc = map[string]*usageAcc{}
	if u.cfg.Store == nil {
		return
	}
	if len(rows) > 0 {
		if err := u.cfg.Store.AddItemUsage(ctx, rows); err != nil {
			u.cfg.Log.Warn("usage: cannot store the hour", "err", err)
		}
	}
	if err := u.cfg.Store.PruneItemUsage(ctx, time.Now().Add(-usageKeep)); err != nil {
		u.cfg.Log.Warn("usage: cannot prune", "err", err)
	}
}
