// Package collect samples system metrics from /proc and /sys (read-only, no
// root) into an in-memory ring buffer and publishes them to live clients.
package collect

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Topic is the WebSocket topic samples are published on.
const Topic = "metrics"

// Defaults from ARCHITECTURE "Sampling": 1 s while a UI watches live
// metrics, 15 s otherwise (enough for alerts). The ring holds 10 min at 1 s.
const (
	FastInterval = time.Second
	SlowInterval = 15 * time.Second
	RingSize     = 600
)

// Sample is one point in time.
type Sample struct {
	Time   time.Time `json:"t"`
	CPU    CPU       `json:"cpu"`
	Mem    Mem       `json:"mem"`
	Disks  []Disk    `json:"disks"`
	DiskIO []DiskIO  `json:"disk_io"`
	Net    []NetIf   `json:"net"`
	Temps  []Temp    `json:"temps"`
	Fans   []Fan     `json:"fans"`
	GPUs   []GPU     `json:"gpus"`
	// CPUTempC is the sensor that best represents the CPU package, if any.
	CPUTempC *float64 `json:"cpu_temp_c,omitempty"`
	// Partial is set on the first sample, which has no CPU deltas yet.
	Partial bool `json:"partial,omitempty"`
}

// Points flattens s into named metrics for the history tables. CPU values
// are left out of a partial sample rather than recorded as zero.
func (s Sample) Points() []store.Point {
	pts := make([]store.Point, 0, 8+len(s.CPU.Cores))
	if !s.Partial {
		pts = append(pts, store.Point{Metric: "cpu.usage", Value: s.CPU.Usage})
		for i, v := range s.CPU.Cores {
			pts = append(pts, store.Point{Metric: "cpu.core." + strconv.Itoa(i), Value: v})
		}
	}
	var sum float64
	var n int
	for _, f := range s.CPU.FreqMHz {
		if f > 0 {
			sum += f
			n++
		}
	}
	if n > 0 {
		pts = append(pts, store.Point{Metric: "cpu.freq", Value: sum / float64(n)})
	}
	for _, d := range s.Disks {
		pts = append(pts, store.Point{Metric: "disk.used:" + d.Mount, Value: float64(d.Used)})
	}
	if !s.Partial {
		for _, d := range s.DiskIO {
			pts = append(pts,
				store.Point{Metric: "disk.read:" + d.Name, Value: d.ReadBps},
				store.Point{Metric: "disk.write:" + d.Name, Value: d.WriteBps})
		}
		for _, n := range s.Net {
			if n.Virtual {
				continue
			}
			pts = append(pts,
				store.Point{Metric: "net.rx:" + n.Name, Value: n.RxBps},
				store.Point{Metric: "net.tx:" + n.Name, Value: n.TxBps})
		}
	}
	if s.CPUTempC != nil {
		pts = append(pts, store.Point{Metric: "cpu.temp", Value: *s.CPUTempC})
	}
	for _, t := range s.Temps {
		pts = append(pts, store.Point{Metric: "temp:" + t.Chip + "/" + t.Label, Value: t.C})
	}
	for _, f := range s.Fans {
		pts = append(pts, store.Point{Metric: "fan:" + f.Chip + "/" + f.Label, Value: f.RPM})
	}
	for _, g := range s.GPUs {
		for _, m := range []struct {
			name string
			v    *float64
		}{{"gpu.util:", g.UtilPct}, {"gpu.mem:", g.MemUsed}, {"gpu.temp:", g.TempC}, {"gpu.power:", g.PowerW}, {"gpu.freq:", g.FreqMHz}} {
			if m.v != nil {
				pts = append(pts, store.Point{Metric: m.name + g.PCI, Value: *m.v})
			}
		}
	}
	return append(pts,
		store.Point{Metric: "load1", Value: s.CPU.Load1},
		store.Point{Metric: "load5", Value: s.CPU.Load5},
		store.Point{Metric: "load15", Value: s.CPU.Load15},
		store.Point{Metric: "mem.used", Value: float64(s.Mem.Used)},
		store.Point{Metric: "mem.cached", Value: float64(s.Mem.Cached)},
		store.Point{Metric: "swap.used", Value: float64(s.Mem.SwapUsed)},
	)
}

// Publisher is the subset of api.Hub the sampler needs.
type Publisher interface {
	Publish(topic string, data any) error
	Subscribers(topic string) int
	Watch(topic string) <-chan struct{}
}

// Config configures a Sampler. Zero values take the defaults above and the
// real /proc and /sys.
type Config struct {
	ProcRoot string
	SysRoot  string
	Fast     time.Duration
	Slow     time.Duration
	Ring     *store.Ring[Sample]
	Pub      Publisher
	Log      *slog.Logger
	// Statfs is replaced in tests.
	Statfs statfsFunc
	// PCIIDs is the pci.ids database used to name GPUs.
	PCIIDs string
	// RunNvidiaSMI is replaced in tests.
	RunNvidiaSMI func(context.Context) ([]byte, error)
	// DiskEvery bounds how often filesystem usage (mountinfo + statfs) is
	// refreshed; usage changes slowly and statfs can be slow on FUSE.
	// Default SlowInterval; negative refreshes every sample.
	DiskEvery time.Duration
	// OnSample, if set, is called with every sample on the sampler
	// goroutine (e.g. to feed the downsampler).
	OnSample func(Sample)
}

// Sampler collects Samples on an adaptive interval.
type Sampler struct {
	cfg      Config
	prev     cpuTimes
	prevCore []cpuTimes
	prevAt   time.Time
	prevDisk map[string]diskCounters
	prevNet  map[string]netCounters
	warned   map[string]bool

	rd      *fileReader
	statfs  *statfsTimeout
	virtual map[string]bool
	disks   []Disk
	disksAt time.Time

	sensors      sensorSet
	gpus         []gpuCard
	discoveredAt time.Time
	nvidia       map[string]GPU
	nvidiaAt     time.Time
	slow         *slowValues
}

// discoverEvery bounds rescans for sensor and GPU files (hotplug is rare).
const discoverEvery = time.Minute

// warnOnce logs each distinct collection error once, not every tick.
func (s *Sampler) warnOnce(err error) {
	if err == nil {
		return
	}
	if s.warned == nil {
		s.warned = map[string]bool{}
	}
	if msg := err.Error(); !s.warned[msg] {
		s.warned[msg] = true
		s.cfg.Log.Warn("collect: section unavailable", "err", err)
	}
}

// New returns a Sampler; call Run to start it.
func New(cfg Config) *Sampler {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.SysRoot == "" {
		cfg.SysRoot = "/sys"
	}
	if cfg.Fast == 0 {
		cfg.Fast = FastInterval
	}
	if cfg.Slow == 0 {
		cfg.Slow = SlowInterval
	}
	if cfg.Ring == nil {
		cfg.Ring = store.NewRing[Sample](RingSize)
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Statfs == nil {
		cfg.Statfs = realStatfs
	}
	if cfg.DiskEvery == 0 {
		cfg.DiskEvery = SlowInterval
	}
	if cfg.PCIIDs == "" {
		cfg.PCIIDs = "/usr/share/misc/pci.ids"
	}
	if cfg.RunNvidiaSMI == nil {
		cfg.RunNvidiaSMI = runNvidiaSMI
	}
	return &Sampler{
		cfg:     cfg,
		rd:      newFileReader(),
		statfs:  newStatfsTimeout(cfg.Statfs, time.Second),
		virtual: map[string]bool{},
		slow:    newSlowValues(5 * time.Second),
	}
}

// Ring returns the buffer samples are written to.
func (s *Sampler) Ring() *store.Ring[Sample] { return s.cfg.Ring }

// Run samples until ctx is cancelled. It samples immediately, then every
// Fast while the topic has subscribers and every Slow otherwise; a change in
// subscribers takes effect at once.
func (s *Sampler) Run(ctx context.Context) {
	var watch <-chan struct{}
	if s.cfg.Pub != nil {
		watch = s.cfg.Pub.Watch(Topic)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			// Subscribers changed: sample now so a new client gets data
			// immediately, then continue at the new rate.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		s.tick()
		timer.Reset(s.interval())
	}
}

func (s *Sampler) interval() time.Duration {
	if s.cfg.Pub != nil && s.cfg.Pub.Subscribers(Topic) > 0 {
		return s.cfg.Fast
	}
	return s.cfg.Slow
}

func (s *Sampler) tick() {
	sample, err := s.Collect()
	if err != nil {
		s.cfg.Log.Warn("collect failed", "err", err)
		return
	}
	s.cfg.Ring.Push(sample)
	if s.cfg.OnSample != nil {
		s.cfg.OnSample(sample)
	}
	if s.cfg.Pub != nil && s.cfg.Pub.Subscribers(Topic) > 0 {
		if err := s.cfg.Pub.Publish(Topic, sample); err != nil {
			s.cfg.Log.Warn("publish failed", "err", err)
		}
	}
}

// Collect reads one Sample. CPU usage is relative to the previous call.
func (s *Sampler) Collect() (Sample, error) {
	total, cores, err := readProcStat(s.rd, s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}
	mem, err := readMemInfo(s.rd, s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}
	l1, l5, l15, err := readLoadAvg(s.rd, s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}

	cpu := CPU{
		Usage:   usage(s.prev, total),
		Cores:   make([]float64, len(cores)),
		FreqMHz: readFreqMHz(s.rd, s.cfg.SysRoot, len(cores)),
		Load1:   l1, Load5: l5, Load15: l15,
	}
	for i, c := range cores {
		if i < len(s.prevCore) {
			cpu.Cores[i] = usage(s.prevCore[i], c)
		}
	}
	// Disk and network are optional: a failure drops that section only.
	now := time.Now()
	if s.cfg.DiskEvery < 0 || now.Sub(s.disksAt) >= s.cfg.DiskEvery {
		disks, err := readDisks(s.rd, s.cfg.ProcRoot, s.statfs.statfs)
		s.warnOnce(err)
		s.disks, s.disksAt = disks, now
	}
	if s.discoveredAt.IsZero() || now.Sub(s.discoveredAt) >= discoverEvery {
		s.sensors = discoverSensors(s.cfg.SysRoot)
		s.gpus = discoverGPUs(s.cfg.SysRoot, s.cfg.PCIIDs)
		s.discoveredAt = now
	}
	temps, fans := s.readSensors()
	gpus := s.readGPUs(context.Background())
	diskC, err := readDiskStats(s.rd, s.cfg.ProcRoot, s.cfg.SysRoot)
	s.warnOnce(err)
	netC, err := readNetDev(s.rd, s.cfg.ProcRoot, s.cfg.SysRoot, s.virtual)
	s.warnOnce(err)

	var dt float64
	if !s.prevAt.IsZero() {
		dt = now.Sub(s.prevAt).Seconds()
	}
	partial := s.prev.total == 0
	sample := Sample{
		Time: now, CPU: cpu, Mem: mem, Partial: partial,
		Disks:  s.disks,
		DiskIO: diskRates(s.prevDisk, diskC, dt),
		Net:    netRates(s.prevNet, netC, dt),
		Temps:  temps,
		Fans:   fans,
		GPUs:   gpus,
	}
	if c, ok := cpuTemp(temps); ok {
		sample.CPUTempC = &c
	}
	s.prev, s.prevCore, s.prevAt, s.prevDisk, s.prevNet = total, cores, now, diskC, netC
	return sample, nil
}
