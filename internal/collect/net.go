package collect

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// NetIf is the throughput of one interface since the previous sample.
type NetIf struct {
	Name  string  `json:"name"`
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
	// Virtual interfaces (bridges, veth, docker0, tun) have no backing
	// device; the UI hides them by default and history skips them.
	Virtual bool `json:"virtual"`
}

type netCounters struct {
	rx, tx  uint64
	virtual bool
}

// readNetDev reads interface counters. virtual caches whether an interface
// has a backing device (interfaces rarely change, so it is looked up once per
// name); entries for vanished interfaces are dropped.
func readNetDev(r *fileReader, procRoot, sysRoot string, virtual map[string]bool) (map[string]netCounters, error) {
	b, err := r.read(procRoot, "net/dev")
	if err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	out := map[string]netCounters{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "lo" || len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("collect: net/dev %s: malformed counters", name)
		}
		v, known := virtual[name]
		if !known {
			_, err := os.Stat(filepath.Join(sysRoot, "class/net", name, "device"))
			v = err != nil
			virtual[name] = v
		}
		out[name] = netCounters{rx: rx, tx: tx, virtual: v}
	}
	for name := range virtual {
		if _, ok := out[name]; !ok {
			delete(virtual, name)
		}
	}
	return out, nil
}

func netRates(prev, cur map[string]netCounters, dt float64) []NetIf {
	out := make([]NetIf, 0, len(cur))
	for name, c := range cur {
		n := NetIf{Name: name, Virtual: c.virtual}
		if p, ok := prev[name]; ok {
			n.RxBps = rate(p.rx, c.rx, dt)
			n.TxBps = rate(p.tx, c.tx, dt)
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Virtual != out[j].Virtual {
			return !out[i].Virtual
		}
		return out[i].Name < out[j].Name
	})
	return out
}
