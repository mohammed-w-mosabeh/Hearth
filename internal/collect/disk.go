package collect

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Disk is the usage of one mounted filesystem, in bytes.
type Disk struct {
	Mount  string  `json:"mount"`
	Device string  `json:"device"`
	FSType string  `json:"fstype"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Avail  uint64  `json:"avail"` // available to unprivileged users
	Pct    float64 `json:"pct"`   // used / (used + avail), like df
}

// DiskIO is the throughput of one whole disk since the previous sample.
type DiskIO struct {
	Name     string  `json:"name"`
	ReadBps  float64 `json:"read_bps"`
	WriteBps float64 `json:"write_bps"`
	BusyPct  float64 `json:"busy_pct"`
}

// realFS are filesystems that hold user data; everything else (proc, tmpfs,
// overlay, squashfs snaps, cgroup...) is skipped.
var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "f2fs": true,
	"zfs": true, "fuseblk": true, "jfs": true, "reiserfs": true, "bcachefs": true,
}

type mount struct {
	devID, root, point, fstype, source string
}

// better reports whether m should represent its device instead of old:
// the mount of the filesystem root wins over bind mounts of subdirectories,
// then the shorter mount point.
func (m mount) better(old mount) bool {
	if (m.root == "/") != (old.root == "/") {
		return m.root == "/"
	}
	return len(m.point) < len(old.point)
}

// readMounts parses mountinfo and returns one mount per device, so bind
// mounts are not counted twice.
func readMounts(r *fileReader, procRoot string) ([]mount, error) {
	b, err := r.read(procRoot, "self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	byDev := map[string]mount{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		// id parent major:minor root mountpoint opts [optional...] - fstype source superopts
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 5 || len(g) < 2 || !realFS[g[0]] {
			continue
		}
		m := mount{devID: f[2], root: unescapeMount(f[3]), point: unescapeMount(f[4]), fstype: g[0], source: unescapeMount(g[1])}
		if old, ok := byDev[m.devID]; !ok || m.better(old) {
			byDev[m.devID] = m
		}
	}
	out := make([]mount, 0, len(byDev))
	for _, m := range byDev {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].point < out[j].point })
	return out, nil
}

// unescapeMount decodes the octal escapes (\040 space, \011 tab, \012
// newline, \134 backslash) the kernel uses in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// statfsFunc is replaced in tests.
type statfsFunc func(path string) (total, free, avail uint64, err error)

func realStatfs(path string) (total, free, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize) // #nosec G115 -- block size is a small positive value
	return st.Blocks * bs, st.Bfree * bs, st.Bavail * bs, nil
}

// statfsTimeout bounds a statfs call: a hung FUSE or network mount must not
// stall the sampler (the classic frozen `df`). At most one call per path is in
// flight; while it is stuck, the path is skipped.
type statfsTimeout struct {
	fn      statfsFunc
	timeout time.Duration

	mu       sync.Mutex
	inflight map[string]bool
}

func newStatfsTimeout(fn statfsFunc, timeout time.Duration) *statfsTimeout {
	return &statfsTimeout{fn: fn, timeout: timeout, inflight: map[string]bool{}}
}

func (s *statfsTimeout) statfs(path string) (total, free, avail uint64, err error) {
	s.mu.Lock()
	if s.inflight[path] {
		s.mu.Unlock()
		return 0, 0, 0, errStatfsStuck
	}
	s.inflight[path] = true
	s.mu.Unlock()

	type result struct {
		total, free, avail uint64
		err                error
	}
	ch := make(chan result, 1)
	go func() {
		t, f, a, err := s.fn(path)
		s.mu.Lock()
		delete(s.inflight, path)
		s.mu.Unlock()
		ch <- result{t, f, a, err}
	}()
	select {
	case r := <-ch:
		return r.total, r.free, r.avail, r.err
	case <-time.After(s.timeout):
		return 0, 0, 0, errStatfsStuck
	}
}

var errStatfsStuck = errors.New("collect: statfs timed out")

func readDisks(r *fileReader, procRoot string, statfs statfsFunc) ([]Disk, error) {
	mounts, err := readMounts(r, procRoot)
	if err != nil {
		return nil, err
	}
	out := make([]Disk, 0, len(mounts))
	for _, m := range mounts {
		total, free, avail, err := statfs(m.point)
		if err != nil || total == 0 {
			continue // unmounted meanwhile, or not visible from our namespace
		}
		used := total - min(free, total)
		d := Disk{Mount: m.point, Device: m.source, FSType: m.fstype, Total: total, Used: used, Avail: avail}
		if used+avail > 0 {
			d.Pct = float64(used) / float64(used+avail) * 100
		}
		out = append(out, d)
	}
	return out, nil
}

type diskCounters struct {
	readSectors, writeSectors, ioMillis uint64
}

// readDiskStats returns counters for whole disks only: devices listed in
// /sys/block, minus loop, ram, zram and optical drives.
func readDiskStats(r *fileReader, procRoot, sysRoot string) (map[string]diskCounters, error) {
	b, err := r.read(procRoot, "diskstats")
	if err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	out := map[string]diskCounters{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 14 {
			continue
		}
		name := f[2]
		if skipBlockDev(name) {
			continue
		}
		if _, err := os.Stat(filepath.Join(sysRoot, "block", name)); err != nil {
			continue // a partition
		}
		var v [3]uint64
		for i, idx := range []int{5, 9, 12} { // sectors read, sectors written, ms doing I/O
			n, err := strconv.ParseUint(f[idx], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("collect: diskstats %s: %w", name, err)
			}
			v[i] = n
		}
		out[name] = diskCounters{readSectors: v[0], writeSectors: v[1], ioMillis: v[2]}
	}
	return out, nil
}

func skipBlockDev(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "fd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// diskRates turns two counter snapshots dt seconds apart into rates. Disks
// absent from prev (first sample, hot-plugged) get zero rates.
func diskRates(prev, cur map[string]diskCounters, dt float64) []DiskIO {
	out := make([]DiskIO, 0, len(cur))
	for name, c := range cur {
		io := DiskIO{Name: name}
		if p, ok := prev[name]; ok && dt > 0 {
			io.ReadBps = rate(p.readSectors, c.readSectors, dt) * 512
			io.WriteBps = rate(p.writeSectors, c.writeSectors, dt) * 512
			io.BusyPct = min(100, rate(p.ioMillis, c.ioMillis, dt)/10) // ms per s -> %
		}
		out = append(out, io)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// rate is the per-second increase of a counter, 0 if it went backwards.
func rate(prev, cur uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}
