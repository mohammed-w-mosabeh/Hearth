package services

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Stat is a unit's resource use from its cgroup.
type Stat struct {
	Mem uint64  `json:"mem"` // memory.current, bytes
	CPU float64 `json:"cpu"` // percent of the whole machine since the last sample
}

// statKey is "<scope>/<unit>", as the UI keys units.
func statKey(scope, name string) string { return scope + "/" + name }

// stats reads unit cgroups under the cgroup root: system units anywhere
// outside the user's manager, user units inside user@<uid>.service.
type stats struct {
	root  string
	uid   int
	ncpu  float64
	prev  map[string]uint64 // usage_usec by key
	prevT time.Time
	buf   []byte
}

func newStats(root string, uid int) *stats {
	return &stats{root: root, uid: uid, ncpu: float64(runtime.NumCPU()), prev: map[string]uint64{}}
}

// unitDir reports whether a cgroup directory is a unit that can hold
// processes (slices only group others; their totals would double count).
func unitDir(name string) bool {
	for _, s := range []string{".service", ".scope", ".socket", ".mount", ".swap"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// sample returns the stats of every unit cgroup with memory in use.
func (s *stats) sample(now time.Time) map[string]Stat {
	out := map[string]Stat{}
	usage := map[string]uint64{}
	userMgr := "user@" + strconv.Itoa(s.uid) + ".service"
	var walk func(rel, scope string, depth int)
	walk = func(rel, scope string, depth int) {
		entries, err := os.ReadDir(filepath.Join(s.root, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			child := rel + "/" + name
			switch {
			case scope == ScopeSystem && name == userMgr:
				s.read(out, usage, child, scope, name)
				walk(child, ScopeUser, 0) // the user's units
			case strings.HasSuffix(name, ".slice") && depth < 4:
				walk(child, scope, depth+1)
			case unitDir(name):
				s.read(out, usage, child, scope, name)
			}
		}
	}
	walk("", ScopeSystem, 0)

	dt := now.Sub(s.prevT).Seconds()
	if !s.prevT.IsZero() && dt > 0 {
		for k, st := range out {
			if p, ok := s.prev[k]; ok && usage[k] >= p {
				st.CPU = float64(usage[k]-p) / 1e6 / dt / s.ncpu * 100
				out[k] = st
			}
		}
	}
	s.prev, s.prevT = usage, now
	return out
}

func (s *stats) read(out map[string]Stat, usage map[string]uint64, rel, scope, name string) {
	dir := filepath.Join(s.root, rel)
	mem, ok := s.readUint(filepath.Join(dir, "memory.current"))
	if !ok || mem == 0 {
		return
	}
	k := statKey(scope, name)
	out[k] = Stat{Mem: mem}
	if b, err := s.readFile(filepath.Join(dir, "cpu.stat")); err == nil {
		if v, ok := field(b, "usage_usec"); ok {
			usage[k] = v
		}
	}
}

func (s *stats) readFile(p string) ([]byte, error) {
	f, err := os.Open(p) // #nosec G304 -- under the cgroup root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if s.buf == nil {
		s.buf = make([]byte, 4096)
	}
	n, err := f.Read(s.buf)
	if err != nil {
		return nil, err
	}
	return s.buf[:n], nil
}

func (s *stats) readUint(p string) (uint64, bool) {
	b, err := s.readFile(p)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(string(bytes.TrimSpace(b)), 10, 64)
	return v, err == nil
}

// field returns the value of "key value" in a flat-keyed cgroup file.
func field(b []byte, key string) (uint64, bool) {
	for _, line := range bytes.Split(b, []byte("\n")) {
		k, v, ok := bytes.Cut(line, []byte(" "))
		if ok && string(k) == key {
			n, err := strconv.ParseUint(string(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}
