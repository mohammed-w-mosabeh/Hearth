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
	Time time.Time `json:"t"`
	CPU  CPU       `json:"cpu"`
	Mem  Mem       `json:"mem"`
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
	// OnSample, if set, is called with every sample on the sampler
	// goroutine (e.g. to feed the downsampler).
	OnSample func(Sample)
}

// Sampler collects Samples on an adaptive interval.
type Sampler struct {
	cfg      Config
	prev     cpuTimes
	prevCore []cpuTimes
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
	return &Sampler{cfg: cfg}
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
	total, cores, err := readProcStat(s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}
	mem, err := readMemInfo(s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}
	l1, l5, l15, err := readLoadAvg(s.cfg.ProcRoot)
	if err != nil {
		return Sample{}, err
	}

	cpu := CPU{
		Usage:   usage(s.prev, total),
		Cores:   make([]float64, len(cores)),
		FreqMHz: readFreqMHz(s.cfg.SysRoot, len(cores)),
		Load1:   l1, Load5: l5, Load15: l15,
	}
	for i, c := range cores {
		if i < len(s.prevCore) {
			cpu.Cores[i] = usage(s.prevCore[i], c)
		}
	}
	partial := s.prev.total == 0
	s.prev, s.prevCore = total, cores
	return Sample{Time: time.Now(), CPU: cpu, Mem: mem, Partial: partial}, nil
}
