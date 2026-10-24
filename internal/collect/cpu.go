package collect

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CPU is one CPU sample. Usage values are percentages since the previous
// sample; they are zero on the first sample.
type CPU struct {
	Usage   float64   `json:"usage"`
	Cores   []float64 `json:"cores"`
	FreqMHz []float64 `json:"freq_mhz"` // 0 where cpufreq is unavailable
	Load1   float64   `json:"load1"`
	Load5   float64   `json:"load5"`
	Load15  float64   `json:"load15"`
}

// cpuTimes are the cumulative jiffies of one /proc/stat "cpu" line.
type cpuTimes struct {
	busy, total uint64
}

// readProcStat returns the aggregate line and one entry per core, in order.
func readProcStat(procRoot string) (cpuTimes, []cpuTimes, error) {
	b, err := readRootFile(procRoot, "stat")
	if err != nil {
		return cpuTimes{}, nil, fmt.Errorf("collect: %w", err)
	}
	var total cpuTimes
	var cores []cpuTimes
	found := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		t, err := parseCPULine(f[1:])
		if err != nil {
			return cpuTimes{}, nil, fmt.Errorf("collect: /proc/stat %s: %w", f[0], err)
		}
		if f[0] == "cpu" {
			total, found = t, true
		} else {
			cores = append(cores, t)
		}
	}
	if !found {
		return cpuTimes{}, nil, fmt.Errorf("collect: /proc/stat has no cpu line")
	}
	return total, cores, nil
}

// parseCPULine reads user nice system idle iowait irq softirq steal
// [guest guest_nice]. guest time is already included in user/nice, so it is
// not added again.
func parseCPULine(f []string) (cpuTimes, error) {
	var v [8]uint64
	for i := 0; i < len(v) && i < len(f); i++ {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return cpuTimes{}, err
		}
		v[i] = n
	}
	idle := v[3] + v[4] // idle + iowait
	var total uint64
	for _, n := range v {
		total += n
	}
	return cpuTimes{busy: total - idle, total: total}, nil
}

// usage returns the busy percentage between two samples of the same CPU.
func usage(prev, cur cpuTimes) float64 {
	if prev.total == 0 || cur.total <= prev.total || cur.busy < prev.busy {
		return 0 // first sample, counter reset or CPU offlined
	}
	return float64(cur.busy-prev.busy) / float64(cur.total-prev.total) * 100
}

func readLoadAvg(procRoot string) (l1, l5, l15 float64, err error) {
	b, err := readRootFile(procRoot, "loadavg")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("collect: %w", err)
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0, fmt.Errorf("collect: malformed loadavg %q", b)
	}
	var vals [3]float64
	for i := range vals {
		if vals[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return 0, 0, 0, fmt.Errorf("collect: loadavg: %w", err)
		}
	}
	return vals[0], vals[1], vals[2], nil
}

// readRootFile reads a fixed file under a /proc or /sys root. Roots come from
// Config (tests use fixtures), never from requests.
func readRootFile(root, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, name)) // #nosec G304 -- fixed name under a configured root
}

// readFreqMHz returns the current frequency of each core, 0 if unknown.
func readFreqMHz(sysRoot string, cores int) []float64 {
	out := make([]float64, cores)
	for i := range out {
		p := filepath.Join("devices/system/cpu", "cpu"+strconv.Itoa(i), "cpufreq/scaling_cur_freq")
		b, err := readRootFile(sysRoot, p)
		if err != nil {
			continue
		}
		if khz, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
			out[i] = khz / 1000
		}
	}
	return out
}
