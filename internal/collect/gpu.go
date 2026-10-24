package collect

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// GPU is one graphics device. Optional readings are nil when the driver does
// not expose them; a sleeping (runtime-suspended) GPU reports only identity,
// because reading its sensors would wake it and cost power.
type GPU struct {
	Card     string   `json:"card"` // card0
	PCI      string   `json:"pci"`  // 0000:01:00.0, stable across boots
	Vendor   string   `json:"vendor"`
	Driver   string   `json:"driver"`
	Name     string   `json:"name"`
	Sleeping bool     `json:"sleeping"`
	UtilPct  *float64 `json:"util_pct,omitempty"`
	MemUsed  *float64 `json:"mem_used,omitempty"` // bytes
	MemTotal *float64 `json:"mem_total,omitempty"`
	TempC    *float64 `json:"temp_c,omitempty"`
	PowerW   *float64 `json:"power_w,omitempty"`
	FreqMHz  *float64 `json:"freq_mhz,omitempty"`
}

type gpuCard struct {
	card, dir, pci, vendor, driver, name string
	hwmon                                string  // relative to sysRoot, "" if none
	powerCapW                            float64 // 0 if the driver exposes no cap
}

var cardRe = regexp.MustCompile(`^card[0-9]+$`)

var vendorNames = map[string]string{"0x8086": "intel", "0x1002": "amd", "0x10de": "nvidia"}

// discoverGPUs lists DRM cards (not connectors like card0-HDMI-A-1).
func discoverGPUs(sysRoot, pciIDs string) []gpuCard {
	entries, _ := os.ReadDir(filepath.Join(sysRoot, "class/drm"))
	var out []gpuCard
	for _, e := range entries {
		if !cardRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join("class/drm", e.Name())
		dev := filepath.Join(sysRoot, dir, "device")
		vendorID := strings.TrimSpace(readSmall(sysRoot, filepath.Join(dir, "device/vendor")))
		deviceID := strings.TrimSpace(readSmall(sysRoot, filepath.Join(dir, "device/device")))
		c := gpuCard{card: e.Name(), dir: dir, vendor: vendorNames[vendorID]}
		if c.vendor == "" {
			c.vendor = vendorID
		}
		if target, err := filepath.EvalSymlinks(dev); err == nil {
			c.pci = filepath.Base(target)
		}
		if drv, err := filepath.EvalSymlinks(filepath.Join(dev, "driver")); err == nil {
			c.driver = filepath.Base(drv)
		}
		c.name = pciName(pciIDs, vendorID, deviceID)
		if hw, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*")); len(hw) > 0 {
			if rel, err := filepath.Rel(sysRoot, hw[0]); err == nil {
				c.hwmon = rel
				c.powerCapW = readMilli(sysRoot, filepath.Join(rel, "power1_cap")) / 1000 // µW -> W
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].card < out[j].card })
	return out
}

// pciName resolves vendor/device IDs ("0x1002", "0x67df") through pci.ids,
// streaming the file so it is never held in memory. Falls back to the IDs.
func pciName(path, vendorID, deviceID string) string {
	v := strings.TrimPrefix(strings.ToLower(vendorID), "0x")
	d := strings.TrimPrefix(strings.ToLower(deviceID), "0x")
	fallback := v + ":" + d
	f, err := os.Open(path) // #nosec G304 -- fixed system database path from Config
	if err != nil {
		return fallback
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	inVendor, vendorName := false, ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		switch {
		case line[0] != '\t':
			if inVendor {
				return vendorName + " " + fallback // vendor block ended without the device
			}
			if id, name, ok := strings.Cut(line, "  "); ok && id == v {
				inVendor, vendorName = true, shortVendor(name)
			}
		case inVendor && len(line) > 1 && line[1] != '\t':
			if id, name, ok := strings.Cut(line[1:], "  "); ok && id == d {
				return vendorName + " " + name
			}
		}
	}
	if inVendor {
		return vendorName + " " + fallback
	}
	return fallback
}

func shortVendor(name string) string {
	switch {
	case strings.HasPrefix(name, "Advanced Micro Devices"):
		return "AMD"
	case strings.HasPrefix(name, "Intel"):
		return "Intel"
	case strings.HasPrefix(name, "NVIDIA"):
		return "NVIDIA"
	}
	return name
}

// maxGPUPowerW bounds plausible GPU power draw when the driver exposes no
// power cap.
const maxGPUPowerW = 1500

// plausiblePower rejects readings no GPU can produce. Some chips report
// garbage (an old GCN laptop chip with a 25 W cap alternated 3.6 W, 1041 W
// and 2087 W); allow up to twice the cap for boost.
func plausiblePower(w, capW float64) bool {
	if w < 0 {
		return false
	}
	if capW > 0 {
		return w <= 2*capW
	}
	return w <= maxGPUPowerW
}

// nvidiaEvery bounds nvidia-smi calls (ROADMAP 1.4: at most every 2 s).
const nvidiaEvery = 2 * time.Second

func (s *Sampler) readGPUs(ctx context.Context) []GPU {
	out := make([]GPU, 0, len(s.gpus))
	wantNvidia := false
	for _, c := range s.gpus {
		g := GPU{Card: c.card, PCI: c.pci, Vendor: c.vendor, Driver: c.driver, Name: c.name}
		if st, err := s.rd.read(s.cfg.SysRoot, filepath.Join(c.dir, "device/power/runtime_status")); err == nil &&
			strings.TrimSpace(string(st)) == "suspended" {
			g.Sleeping = true
			out = append(out, g)
			continue
		}
		switch c.driver {
		case "amdgpu", "radeon":
			g.UtilPct = s.optNumber(filepath.Join(c.dir, "device/gpu_busy_percent"), 1)
			g.MemUsed = s.optNumber(filepath.Join(c.dir, "device/mem_info_vram_used"), 1)
			g.MemTotal = s.optNumber(filepath.Join(c.dir, "device/mem_info_vram_total"), 1)
		case "i915", "xe":
			g.FreqMHz = s.optNumber(filepath.Join(c.dir, "gt_act_freq_mhz"), 1)
		case "nvidia":
			wantNvidia = true
		}
		if c.hwmon != "" {
			g.TempC = s.optNumber(filepath.Join(c.hwmon, "temp1_input"), 1000)
			// Power readings can block for a measurement window (amdgpu
			// power1_input: ~500 ms), so they are read in the background.
			ok := func(w float64) bool { return plausiblePower(w, c.powerCapW) }
			if g.PowerW = s.slow.get(filepath.Join(s.cfg.SysRoot, c.hwmon, "power1_average"), 1e6, ok); g.PowerW == nil {
				g.PowerW = s.slow.get(filepath.Join(s.cfg.SysRoot, c.hwmon, "power1_input"), 1e6, ok)
			}
			if g.FreqMHz == nil { // amdgpu: freq1 is the shader clock, in Hz
				g.FreqMHz = s.optNumber(filepath.Join(c.hwmon, "freq1_input"), 1e6)
			}
		}
		out = append(out, g)
	}
	if wantNvidia {
		s.fillNvidia(ctx, out)
	}
	return out
}

// slowValues caches sysfs numbers that can be too slow to read inline. get
// returns the last value at once and refreshes it in the background at most
// every refresh, one read per path at a time. Reads that fail, return an
// implausible value or take longer than slowRead count as strikes; after
// maxStrikes the attribute is never read again. This matters because a slow
// attribute holds a driver lock (amdgpu power1_input on an old GCN chip:
// ~500 ms) and stalls the device's other sensors too.
type slowValues struct {
	refresh time.Duration

	mu sync.Mutex
	m  map[string]*slowValue
}

type slowValue struct {
	val      *float64
	at       time.Time
	inflight bool
	strikes  int
}

const (
	slowRead   = 100 * time.Millisecond
	maxStrikes = 3
)

func newSlowValues(refresh time.Duration) *slowValues {
	return &slowValues{refresh: refresh, m: map[string]*slowValue{}}
}

func (sv *slowValues) get(path string, scale float64, valid func(float64) bool) *float64 {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	v := sv.m[path]
	if v == nil {
		v = &slowValue{}
		sv.m[path] = v
	}
	if v.strikes < maxStrikes && !v.inflight && time.Since(v.at) >= sv.refresh {
		v.inflight = true
		go sv.refreshValue(v, path, scale, valid)
	}
	if v.val == nil || v.strikes >= maxStrikes {
		return nil
	}
	c := *v.val
	return &c
}

func (sv *slowValues) refreshValue(v *slowValue, path string, scale float64, valid func(float64) bool) {
	start := time.Now()
	var out *float64
	if b, err := readFile(path); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
			f /= scale
			if valid == nil || valid(f) {
				out = &f
			}
		}
	}
	slow := time.Since(start) > slowRead
	sv.mu.Lock()
	defer sv.mu.Unlock()
	v.at, v.inflight = time.Now(), false
	if out == nil || slow {
		v.strikes++
		v.val = nil
		return
	}
	v.strikes, v.val = 0, out
}

// optNumber reads a sysfs number divided by scale, or nil if unavailable
// (some drivers return EBADF/ENODATA for unsupported sensors).
func (s *Sampler) optNumber(rel string, scale float64) *float64 {
	v, ok := s.readNumber(rel)
	if !ok {
		return nil
	}
	v /= scale
	return &v
}

// nvidiaQuery is the only nvidia-smi invocation (see safeexec allowlist).
const nvidiaQuery = "--query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw"

func (s *Sampler) fillNvidia(ctx context.Context, gpus []GPU) {
	if time.Since(s.nvidiaAt) >= nvidiaEvery {
		s.nvidiaAt = time.Now()
		res, err := s.cfg.RunNvidiaSMI(ctx)
		s.warnOnce(err)
		if err == nil {
			s.nvidia = parseNvidiaSMI(string(res))
		}
	}
	for i := range gpus {
		if gpus[i].Vendor != "nvidia" || gpus[i].Sleeping {
			continue
		}
		if n, ok := s.nvidia[strings.ToLower(gpus[i].PCI)]; ok {
			gpus[i].Name, gpus[i].UtilPct, gpus[i].MemUsed, gpus[i].MemTotal, gpus[i].TempC, gpus[i].PowerW =
				n.Name, n.UtilPct, n.MemUsed, n.MemTotal, n.TempC, n.PowerW
		}
	}
}

func runNvidiaSMI(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := safeexec.Run(ctx, "nvidia-smi", nvidiaQuery, "--format=csv,noheader,nounits")
	return res.Stdout, err
}

// parseNvidiaSMI maps normalized PCI address -> readings. nvidia-smi prints
// an 8-digit PCI domain ("00000000:01:00.0"); sysfs uses 4.
func parseNvidiaSMI(out string) map[string]GPU {
	m := map[string]GPU{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ",")
		if len(f) != 7 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		bus := strings.ToLower(f[0])
		if len(bus) > 12 {
			bus = bus[len(bus)-12:]
		}
		mib := func(s string) *float64 {
			v := num(s)
			if v != nil {
				*v *= 1 << 20
			}
			return v
		}
		m[bus] = GPU{Name: f[1], UtilPct: num(f[2]), MemUsed: mib(f[3]), MemTotal: mib(f[4]), TempC: num(f[5]), PowerW: num(f[6])}
	}
	return m
}

// num parses a value, treating "[N/A]" / "[Not Supported]" as missing.
func num(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
