package docker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// StatsTopic carries the resource use of running containers. Docker is
// asked only while it has subscribers; the history (last HistoryLen
// samples per container) stays in memory between visits.
const StatsTopic = "docker.stats"

// HistoryLen is how many samples are kept per container (5 min at 2 s).
const HistoryLen = 150

// maxSampled bounds the containers sampled per tick, and sampleWorkers
// the broker calls at once.
const (
	maxSampled    = 64
	sampleWorkers = 4
)

// Point is one container's resource use at a time. Rates are per second
// since the previous sample; the first sample of a container has none.
type Point struct {
	T        int64   `json:"t"`   // Unix ms
	CPU      float64 `json:"cpu"` // percent of one CPU (100 = one full core)
	Mem      uint64  `json:"mem"` // bytes, page cache excluded
	MemLimit uint64  `json:"mem_limit"`
	NetRx    float64 `json:"net_rx"` // bytes/s
	NetTx    float64 `json:"net_tx"`
	BlkRead  float64 `json:"blk_read"` // bytes/s
	BlkWrite float64 `json:"blk_write"`
	Pids     uint64  `json:"pids"`
}

// StatsMsg is a message on StatsTopic: "history" (every container's
// samples, sent to new subscribers) or "sample" (the latest of each).
type StatsMsg struct {
	Kind    string             `json:"kind"`
	History map[string][]Point `json:"history,omitempty"`
	Sample  map[string]Point   `json:"sample,omitempty"`
}

// counters are a sample's raw totals, for the next one's rates.
type counters struct {
	t                         time.Time
	cpu, system               uint64
	rx, tx, blkRead, blkWrite uint64
}

// Stats samples running containers while StatsTopic has subscribers.
type Stats struct {
	b        Broker
	pub      Publisher
	log      *slog.Logger
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	history map[string][]Point
	last    map[string]counters
}

// NewStats returns a Stats sampling every interval (default 2 s); call Run.
func NewStats(b Broker, pub Publisher, log *slog.Logger, interval time.Duration) *Stats {
	if log == nil {
		log = slog.Default()
	}
	if interval == 0 {
		interval = 2 * time.Second
	}
	return &Stats{b: b, pub: pub, log: log, interval: interval, now: time.Now,
		history: map[string][]Point{}, last: map[string]counters{}}
}

// Run samples until ctx ends.
func (s *Stats) Run(ctx context.Context) {
	watch := s.pub.Watch(StatsTopic)
	subs := 0
	var tick <-chan time.Time
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			n := s.pub.Subscribers(StatsTopic)
			if n > subs {
				_ = s.pub.Publish(StatsTopic, StatsMsg{Kind: "history", History: s.History()})
			}
			subs = n
			switch {
			case n > 0 && ticker == nil:
				ticker = time.NewTicker(s.interval)
				tick = ticker.C
				s.sample(ctx)
			case n == 0 && ticker != nil:
				ticker.Stop()
				ticker, tick = nil, nil
			}
		case <-tick:
			s.sample(ctx)
		}
	}
}

// History returns a copy of every container's samples.
func (s *Stats) History() map[string][]Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]Point, len(s.history))
	for id, h := range s.history {
		out[id] = append([]Point(nil), h...)
	}
	return out
}

// rawStats is the part of a one-shot stats answer Hearth reads.
type rawStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// sample takes one sample of every running container and publishes it.
func (s *Stats) sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.interval*3)
	defer cancel()
	var list []struct {
		ID string `json:"Id"`
	}
	if err := s.b.Call(ctx, "docker.containers.list", map[string]bool{"all": false}, &list); err != nil {
		s.log.Debug("docker: stats list", "err", err)
		return
	}
	if len(list) > maxSampled {
		list = list[:maxSampled]
	}
	type got struct {
		id  string
		raw rawStats
		t   time.Time
	}
	results := make(chan got, len(list))
	ids := make(chan string)
	var wg sync.WaitGroup
	for range min(sampleWorkers, len(list)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				var raw json.RawMessage
				if err := s.b.Call(ctx, "docker.containers.stats", map[string]string{"id": id}, &raw); err != nil {
					continue // stopped meanwhile
				}
				var r rawStats
				if json.Unmarshal(raw, &r) == nil {
					results <- got{id, r, s.now()}
				}
			}
		}()
	}
	for _, c := range list {
		ids <- c.ID
	}
	close(ids)
	wg.Wait()
	close(results)

	sample := map[string]Point{}
	s.mu.Lock()
	for g := range results {
		p, c := point(g.raw, g.t, s.last[g.id])
		s.last[g.id] = c
		h := append(s.history[g.id], p)
		if len(h) > HistoryLen {
			h = h[len(h)-HistoryLen:]
		}
		s.history[g.id] = h
		sample[g.id] = p
	}
	// Containers no longer running keep their history until it is old.
	cutoff := s.now().Add(-time.Duration(HistoryLen) * s.interval).UnixMilli()
	for id, h := range s.history {
		if _, ok := sample[id]; !ok && h[len(h)-1].T < cutoff {
			delete(s.history, id)
			delete(s.last, id)
		}
	}
	s.mu.Unlock()
	_ = s.pub.Publish(StatsTopic, StatsMsg{Kind: "sample", Sample: sample})
}

// point computes a Point from raw totals and the previous ones.
func point(r rawStats, t time.Time, prev counters) (Point, counters) {
	c := counters{t: t, cpu: r.CPUStats.CPUUsage.TotalUsage, system: r.CPUStats.SystemUsage}
	for _, n := range r.Networks {
		c.rx += n.RxBytes
		c.tx += n.TxBytes
	}
	for _, e := range r.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			c.blkRead += e.Value
		case "write":
			c.blkWrite += e.Value
		}
	}
	mem := r.MemoryStats.Usage
	// Page cache is not the container's own memory (what `docker stats`
	// subtracts): inactive_file on cgroup v2, total_inactive_file on v1.
	cache := r.MemoryStats.Stats["inactive_file"]
	if v, ok := r.MemoryStats.Stats["total_inactive_file"]; ok {
		cache = v
	}
	if cache < mem {
		mem -= cache
	}
	p := Point{T: t.UnixMilli(), Mem: mem, MemLimit: r.MemoryStats.Limit, Pids: r.PidsStats.Current}
	if prev.t.IsZero() {
		return p, c
	}
	if c.system > prev.system && c.cpu >= prev.cpu {
		cpus := float64(max(r.CPUStats.OnlineCPUs, 1))
		p.CPU = float64(c.cpu-prev.cpu) / float64(c.system-prev.system) * cpus * 100
	}
	if secs := t.Sub(prev.t).Seconds(); secs > 0 {
		rate := func(now, before uint64) float64 {
			if now < before {
				return 0 // counters reset (restart)
			}
			return float64(now-before) / secs
		}
		p.NetRx, p.NetTx = rate(c.rx, prev.rx), rate(c.tx, prev.tx)
		p.BlkRead, p.BlkWrite = rate(c.blkRead, prev.blkRead), rate(c.blkWrite, prev.blkWrite)
	}
	return p, c
}
