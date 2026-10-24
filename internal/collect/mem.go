package collect

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Mem is one memory sample, in bytes.
type Mem struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	Used      uint64 `json:"used"` // Total - Available, what "used" means to a user
	Cached    uint64 `json:"cached"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
}

func readMemInfo(procRoot string) (Mem, error) {
	b, err := readRootFile(procRoot, "meminfo")
	if err != nil {
		return Mem{}, fmt.Errorf("collect: %w", err)
	}
	kv := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		kv[key] = n
	}
	total, ok := kv["MemTotal"]
	if !ok || total == 0 {
		return Mem{}, fmt.Errorf("collect: meminfo has no MemTotal")
	}
	avail, ok := kv["MemAvailable"]
	if !ok {
		// Kernels before 3.14: approximate.
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	avail = min(avail, total)
	m := Mem{
		Total:     total,
		Available: avail,
		Used:      total - avail,
		Cached:    kv["Cached"],
		SwapTotal: kv["SwapTotal"],
	}
	if free := kv["SwapFree"]; free <= m.SwapTotal {
		m.SwapUsed = m.SwapTotal - free
	}
	return m, nil
}
