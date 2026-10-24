package collect

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	f.write(f.proc, "self/mountinfo", strings.Join([]string{
		`24 31 0:22 / /sys rw,nosuid - sysfs sysfs rw`,
		`31 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw`,
		`48 31 8:1 / /boot/efi rw,relatime shared:31 - vfat /dev/sda1 rw`,
		`51 31 8:4 / /home rw,relatime shared:53 - ext4 /dev/sda4 rw`,
		`54 31 8:4 /opt-data /opt rw,relatime shared:53 - ext4 /dev/sda4 rw`, // bind of sda4
		`60 31 0:40 / /tmp rw - tmpfs tmpfs rw`,
		`61 31 7:3 / /snap/core/1 ro - squashfs /dev/loop3 ro`,
		`62 31 8:17 / /media/My\040Disk rw - exfat /dev/sdb1 rw`,
		`63 31 0:50 / /var/lib/docker/overlay2/x/merged rw - overlay overlay rw`,
	}, "\n")+"\n")
	f.diskstats(0, 0, 0)
	for _, d := range []string{"block/sda", "block/sdb", "block/loop3"} {
		f.write(f.sys, d+"/size", "1\n")
	}
	f.netdev(0, 0)
	f.write(f.sys, "class/net/wlp3s0/device/uevent", "\n")
	return f
}

// diskstats writes sda counters (sectors read/written, io ms) plus a
// partition and a loop device that must be ignored.
func (f *fixture) diskstats(rd, wr, ioms uint64) {
	f.write(f.proc, "diskstats",
		"   8       0 sda 100 0 "+u(1000+rd)+" 0 50 0 "+u(2000+wr)+" 0 0 "+u(500+ioms)+" 0 0 0 0 0 0 0\n"+
			"   8       2 sda2 90 0 900 0 40 0 1800 0 0 400 0 0 0 0 0 0 0\n"+
			"   7       3 loop3 5 0 50 0 0 0 0 0 0 1 0 0 0 0 0 0 0\n"+
			"   8      16 sdb 1 0 8 0 1 0 8 0 0 1 0 0 0 0 0 0 0\n")
}

func (f *fixture) netdev(rx, tx uint64) {
	f.write(f.proc, "net/dev", `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0
  wlp3s0: `+u(10000+rx)+` 10 0 0 0 0 0 0 `+u(20000+tx)+` 20 0 0 0 0 0 0
docker0: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0
`)
}

func fakeStatfs(path string) (total, free, avail uint64, err error) {
	switch path {
	case "/":
		return 100 << 30, 40 << 30, 35 << 30, nil // 5 GiB reserved for root
	case "/home", "/boot/efi", "/media/My Disk":
		return 10 << 30, 5 << 30, 5 << 30, nil
	}
	return 0, 0, 0, os.ErrNotExist
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
	s := New(Config{ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Statfs: fakeStatfs})

	first, err := s.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPU.Usage != 0 || first.CPU.Cores[0] != 0 || !first.Partial {
		t.Fatalf("first sample usage %v %v partial %v", first.CPU.Usage, first.CPU.Cores, first.Partial)
	}
	for _, p := range first.Points() {
		if strings.HasPrefix(p.Metric, "cpu.usage") || strings.HasPrefix(p.Metric, "cpu.core") {
			t.Fatalf("partial sample recorded %s", p.Metric)
		}
	}

	f.stat(25, 75, 100)
	f.diskstats(2048, 4096, 250)
	f.netdev(1000, 3000)
	time.Sleep(20 * time.Millisecond) // rates need elapsed time
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

	if got.Partial {
		t.Fatal("second sample marked partial")
	}
	pts := map[string]float64{}
	for _, p := range got.Points() {
		pts[p.Metric] = p.Value
	}
	if !near(pts["cpu.usage"], 50) || !near(pts["cpu.core.1"], 75) || !near(pts["cpu.freq"], 2400) ||
		pts["load1"] != 0.52 || pts["mem.used"] != 10000000*1024 || pts["swap.used"] != 500000*1024 ||
		pts["disk.used:/"] != 60<<30 || pts["disk.read:sda"] <= 0 || pts["net.rx:wlp3s0"] <= 0 {
		t.Fatalf("points %v", pts)
	}
	if _, ok := pts["net.rx:docker0"]; ok {
		t.Fatal("virtual interface recorded in history")
	}

	m := got.Mem
	const k = 1024
	if m.Total != 16000000*k || m.Available != 6000000*k || m.Used != 10000000*k ||
		m.Cached != 4000000*k || m.SwapTotal != 2000000*k || m.SwapUsed != 500000*k {
		t.Fatalf("mem %+v", m)
	}
}

func TestDisksAndNet(t *testing.T) {
	f := newFixture(t)
	rd := newFileReader()
	disks, err := readDisks(rd, f.proc, fakeStatfs)
	if err != nil {
		t.Fatal(err)
	}
	var mounts []string
	for _, d := range disks {
		mounts = append(mounts, d.Mount)
	}
	// /opt is a bind of the /home device; tmpfs/squashfs/overlay skipped;
	// \040 decoded.
	if want := []string{"/", "/boot/efi", "/home", "/media/My Disk"}; strings.Join(mounts, "|") != strings.Join(want, "|") {
		t.Fatalf("mounts %q, want %q", mounts, want)
	}
	root := disks[0]
	if root.Used != 60<<30 || root.Avail != 35<<30 || !near(root.Pct, 60.0/95*100) || root.Device != "/dev/sda2" || root.FSType != "ext4" {
		t.Fatalf("root %+v", root)
	}

	prevD, _ := readDiskStats(rd, f.proc, f.sys)
	if _, ok := prevD["sda2"]; ok {
		t.Fatal("partition counted as a disk")
	}
	if _, ok := prevD["loop3"]; ok {
		t.Fatal("loop device counted")
	}
	f.diskstats(2048, 4096, 500)
	curD, _ := readDiskStats(rd, f.proc, f.sys)
	io := diskRates(prevD, curD, 2)
	if len(io) != 2 || io[0].Name != "sda" || io[0].ReadBps != 2048*512/2 || io[0].WriteBps != 4096*512/2 || io[0].BusyPct != 25 {
		t.Fatalf("disk io %+v", io)
	}

	virt := map[string]bool{}
	prevN, _ := readNetDev(rd, f.proc, f.sys, virt)
	if _, ok := prevN["lo"]; ok {
		t.Fatal("lo included")
	}
	f.netdev(4000, 8000)
	curN, _ := readNetDev(rd, f.proc, f.sys, virt)
	nets := netRates(prevN, curN, 4)
	if len(nets) != 2 || nets[0].Name != "wlp3s0" || nets[0].Virtual || nets[0].RxBps != 1000 || nets[0].TxBps != 2000 ||
		nets[1].Name != "docker0" || !nets[1].Virtual {
		t.Fatalf("net %+v", nets)
	}

	// Counter reset (e.g. driver reload) yields 0, not a huge number.
	if r := netRates(curN, prevN, 1); r[0].RxBps != 0 {
		t.Fatalf("reset rate %v", r[0].RxBps)
	}
}

func TestStatfsTimeout(t *testing.T) {
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	hung := func(path string) (uint64, uint64, uint64, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return 1, 1, 1, nil
	}
	st := newStatfsTimeout(hung, 20*time.Millisecond)
	start := time.Now()
	if _, _, _, err := st.statfs("/stuck"); err == nil {
		t.Fatal("hung statfs returned no error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not applied")
	}
	// While the first call is still stuck, no second goroutine is started.
	if _, _, _, err := st.statfs("/stuck"); err == nil {
		t.Fatal("stuck mount not skipped")
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("%d calls in flight for one stuck mount", calls)
	}
	mu.Unlock()
	close(release)
	waitFor(t, func() bool {
		total, _, _, err := st.statfs("/stuck")
		return err == nil && total == 1
	})
}

func TestDiskUsageRefreshRate(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	counting := func(p string) (uint64, uint64, uint64, error) {
		calls.Add(1)
		return fakeStatfs(p)
	}
	s := New(Config{ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Statfs: counting, DiskEvery: time.Hour})
	for range 5 {
		got, err := s.Collect()
		if err != nil || len(got.Disks) != 4 {
			t.Fatalf("%v %v", got.Disks, err)
		}
	}
	if n := calls.Load(); n != 4 { // 4 mounts (after dedup) statted once, not on every sample
		t.Fatalf("statfs called %d times for 4 mounts over 5 samples", n)
	}
}

func TestMissingSectionsAreNotFatal(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"diskstats", "net/dev", "self/mountinfo"} {
		if err := os.Remove(filepath.Join(f.proc, p)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(Config{ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Statfs: fakeStatfs}).Collect()
	if err != nil {
		t.Fatalf("missing optional sections failed the sample: %v", err)
	}
	if got.Mem.Total == 0 || len(got.Disks) != 0 || len(got.Net) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestUnescapeMount(t *testing.T) {
	for in, want := range map[string]string{
		`/plain`: "/plain", `/a\040b`: "/a b", `/t\011ab`: "/t\tab", `/back\134slash`: `/back\slash`, `/bad\9`: `/bad\9`,
	} {
		if got := unescapeMount(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
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
	m, err := readMemInfo(newFileReader(), f.proc)
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
	rd := newFileReader()
	if _, err := readMemInfo(rd, f.proc); err == nil {
		t.Error("meminfo without MemTotal accepted")
	}
	f.write(f.proc, "stat", "intr 1\n")
	if _, _, err := readProcStat(rd, f.proc); err == nil {
		t.Error("stat without cpu line accepted")
	}
	f.write(f.proc, "stat", "cpu  1 2 x 4 5 6 7 8\n")
	if _, _, err := readProcStat(rd, f.proc); err == nil {
		t.Error("non-numeric stat accepted")
	}
	f.write(f.proc, "loadavg", "0.1\n")
	if _, _, _, err := readLoadAvg(rd, f.proc); err == nil {
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
	hasRoot := false
	for _, d := range got.Disks {
		t.Logf("disk %-20s %-6s %5.1f%% of %d GiB (%s)", d.Mount, d.FSType, d.Pct, d.Total>>30, d.Device)
		hasRoot = hasRoot || d.Mount == "/"
	}
	if !hasRoot {
		t.Error("/ not among disks")
	}
	for _, d := range got.DiskIO {
		t.Logf("disk io %s r=%.0f B/s w=%.0f B/s busy=%.1f%%", d.Name, d.ReadBps, d.WriteBps, d.BusyPct)
	}
	phys := 0
	for _, n := range got.Net {
		if !n.Virtual {
			phys++
			t.Logf("net %s rx=%.0f B/s tx=%.0f B/s", n.Name, n.RxBps, n.TxBps)
		}
	}
	t.Logf("%d interfaces, %d physical", len(got.Net), phys)
	for _, tp := range got.Temps {
		t.Logf("temp %s/%s %.1f°C", tp.Chip, tp.Label, tp.C)
	}
	for _, f := range got.Fans {
		t.Logf("fan %s/%s %.0f rpm", f.Chip, f.Label, f.RPM)
	}
	if got.CPUTempC != nil {
		t.Logf("cpu temp %.1f°C", *got.CPUTempC)
	}
	for _, g := range got.GPUs {
		f := func(p *float64) string {
			if p == nil {
				return "-"
			}
			return strconv.FormatFloat(*p, 'f', 1, 64)
		}
		t.Logf("gpu %s %s %s sleeping=%v util=%s temp=%s power=%s freq=%s", g.Card, g.Driver, g.Name, g.Sleeping, f(g.UtilPct), f(g.TempC), f(g.PowerW), f(g.FreqMHz))
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
		ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Pub: pub, Ring: ring, Statfs: fakeStatfs,
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
