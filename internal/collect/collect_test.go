package collect

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fixture struct {
	t    *testing.T
	proc string
	sys  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, proc: filepath.Join(dir, "proc"), sys: filepath.Join(dir, "sys")}
	f.write(f.proc, "loadavg", "0.52 0.41 0.30 2/1234 5678\n")
	f.write(f.proc, "meminfo", `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    6000000 kB
Buffers:          500000 kB
Cached:          4000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
HugePages_Total:       0
`)
	f.stat(0, 0, 0)
	f.write(f.sys, "devices/system/cpu/cpu0/cpufreq/scaling_cur_freq", "2400000\n")
	// cpu1 has no cpufreq directory (common in VMs).
	return f
}

func (f *fixture) write(root, rel, data string) {
	f.t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// stat writes /proc/stat where, elapsed jiffies after a baseline, core 0 has
// been busy for busy0 of them and core 1 for busy1.
func (f *fixture) stat(busy0, busy1, elapsed uint64) {
	line := func(name string, user, idle uint64) string {
		// user nice system idle iowait irq softirq steal guest guest_nice
		return name + " " + u(user) + " 0 100 " + u(idle) + " 10 0 0 0 50 0\n"
	}
	b0, b1 := 1000+busy0, 1000+busy1
	i0, i1 := 5000+elapsed-busy0, 5000+elapsed-busy1
	f.write(f.proc, "stat", line("cpu ", b0+b1, i0+i1)+line("cpu0", b0, i0)+line("cpu1", b1, i1)+
		"intr 12345\nctxt 999\nbtime 1700000000\n")
}

func u(n uint64) string { return strconv.FormatUint(n, 10) }

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestCollect(t *testing.T) {
	f := newFixture(t)
	s := New(Config{ProcRoot: f.proc, SysRoot: f.sys, Log: discard()})

	first, err := s.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPU.Usage != 0 || first.CPU.Cores[0] != 0 {
		t.Fatalf("first sample usage %v %v, want 0", first.CPU.Usage, first.CPU.Cores)
	}

	f.stat(25, 75, 100)
	got, err := s.Collect()
	if err != nil {
		t.Fatal(err)
	}
	c := got.CPU
	if len(c.Cores) != 2 || !near(c.Cores[0], 25) || !near(c.Cores[1], 75) || !near(c.Usage, 50) {
		t.Fatalf("usage %v cores %v", c.Usage, c.Cores)
	}
	if !near(c.FreqMHz[0], 2400) || c.FreqMHz[1] != 0 {
		t.Fatalf("freq %v", c.FreqMHz)
	}
	if c.Load1 != 0.52 || c.Load5 != 0.41 || c.Load15 != 0.30 {
		t.Fatalf("load %v %v %v", c.Load1, c.Load5, c.Load15)
	}

	m := got.Mem
	const k = 1024
	if m.Total != 16000000*k || m.Available != 6000000*k || m.Used != 10000000*k ||
		m.Cached != 4000000*k || m.SwapTotal != 2000000*k || m.SwapUsed != 500000*k {
		t.Fatalf("mem %+v", m)
	}
}

func TestUsageEdgeCases(t *testing.T) {
	if usage(cpuTimes{}, cpuTimes{busy: 10, total: 100}) != 0 {
		t.Error("first sample not 0")
	}
	if usage(cpuTimes{busy: 50, total: 100}, cpuTimes{busy: 10, total: 20}) != 0 {
		t.Error("counter reset not 0")
	}
	if usage(cpuTimes{busy: 50, total: 100}, cpuTimes{busy: 50, total: 100}) != 0 {
		t.Error("no elapsed time not 0")
	}
}

func TestMemInfoWithoutMemAvailable(t *testing.T) {
	f := newFixture(t)
	f.write(f.proc, "meminfo", "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	m, err := readMemInfo(f.proc)
	if err != nil {
		t.Fatal(err)
	}
	if m.Available != 400*1024 || m.Used != 600*1024 || m.SwapUsed != 0 {
		t.Fatalf("%+v", m)
	}
}

func TestMalformedInputs(t *testing.T) {
	f := newFixture(t)
	f.write(f.proc, "meminfo", "nothing useful\n")
	if _, err := readMemInfo(f.proc); err == nil {
		t.Error("meminfo without MemTotal accepted")
	}
	f.write(f.proc, "stat", "intr 1\n")
	if _, _, err := readProcStat(f.proc); err == nil {
		t.Error("stat without cpu line accepted")
	}
	f.write(f.proc, "stat", "cpu  1 2 x 4 5 6 7 8\n")
	if _, _, err := readProcStat(f.proc); err == nil {
		t.Error("non-numeric stat accepted")
	}
	f.write(f.proc, "loadavg", "0.1\n")
	if _, _, _, err := readLoadAvg(f.proc); err == nil {
		t.Error("short loadavg accepted")
	}
}

func TestCollectRealSystem(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	s := New(Config{Log: discard()})
	if _, err := s.Collect(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	got, err := s.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if got.Mem.Total == 0 || len(got.CPU.Cores) == 0 || got.CPU.Usage < 0 || got.CPU.Usage > 100 {
		t.Fatalf("implausible sample %+v", got)
	}
}

// fakePub stands in for api.Hub.
type fakePub struct {
	mu        sync.Mutex
	subs      int
	published int
	watch     chan struct{}
}

func (p *fakePub) Publish(string, any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published++
	return nil
}

func (p *fakePub) Subscribers(string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subs
}

func (p *fakePub) Watch(string) <-chan struct{} { return p.watch }

func (p *fakePub) setSubs(n int) {
	p.mu.Lock()
	p.subs = n
	p.mu.Unlock()
	p.watch <- struct{}{}
}

func (p *fakePub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published
}

func TestSamplerAdaptiveInterval(t *testing.T) {
	f := newFixture(t)
	pub := &fakePub{watch: make(chan struct{}, 1)}
	ring := store.NewRing[Sample](1000)
	s := New(Config{
		ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Pub: pub, Ring: ring,
		Fast: 5 * time.Millisecond, Slow: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Immediate first sample, then slow: nothing more for a while.
	waitFor(t, func() bool { return ring.Len() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := ring.Len(); n != 1 {
		t.Fatalf("sampled %d times with no subscribers and a 1h slow interval", n)
	}
	if pub.count() != 0 {
		t.Fatal("published with no subscribers")
	}

	// A subscriber arrives: sample at once and then fast, publishing each.
	pub.setSubs(1)
	waitFor(t, func() bool { return ring.Len() >= 10 && pub.count() >= 9 })

	// Subscriber leaves: back to slow.
	pub.setSubs(0)
	time.Sleep(20 * time.Millisecond)
	n := ring.Len()
	time.Sleep(50 * time.Millisecond)
	if ring.Len() != n {
		t.Fatalf("still sampling fast after unsubscribe: %d -> %d", n, ring.Len())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
