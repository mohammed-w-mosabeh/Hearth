package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// UsageTopic carries running containers' CPU and memory for the Home
// card (ROADMAP 2.11).
const UsageTopic = "docker.usage"

// Usage is a container's resource use, read from its cgroup.
type Usage struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"` // percent of one CPU; absent on the first reading
	Mem  uint64  `json:"mem"` // bytes, inactive page cache excluded
}

// UsageMsg is published on UsageTopic and served by HandleUsage.
type UsageMsg struct {
	Available  bool     `json:"available"` // Docker is installed
	Running    []Usage  `json:"running"`   // by CPU, busiest first
	Stopped    int      `json:"stopped"`   // created, exited or dead
	Unexpected []string `json:"unexpected"`
	Time       int64    `json:"time"` // Unix ms
}

// Monitor follows containers through their cgroups, which hearthd can
// read without the broker. The broker is asked for the container list
// only when the running set changes (a container started, stopped or
// restarted), so it still exits when idle. Unexpected exits go to Observe
// as the "container.exited" alert metric. The list is also read every
// Refresh, which catches a removed container, or one that started and
// failed between two readings.
type Monitor struct {
	B          Broker
	Pub        Publisher
	Log        *slog.Logger
	CgroupRoot string        // default /sys/fs/cgroup
	Socket     string        // default /var/run/docker.sock; absent: Docker is not installed
	Idle       time.Duration // default 15 s, without UsageTopic subscribers
	Busy       time.Duration // default 2 s, with them
	Refresh    time.Duration // default 5 min
	Observe    func(ctx context.Context, now time.Time, values map[string]float64)

	mu      sync.Mutex
	last    UsageMsg
	scopes  map[string]scope  // running container id -> its cgroup
	names   map[string]string // container id -> name
	stopped int
	unexp   []string
	known   bool      // the list has been read at least once
	read    time.Time // when it was last read, or last failed
}

type scope struct {
	dir   string
	ino   uint64 // a restarted container gets a new cgroup directory
	usec  uint64 // cpu.stat usage_usec
	taken time.Time
}

// Run samples until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	idle, busy := m.Idle, m.Busy
	if idle == 0 {
		idle = 15 * time.Second
	}
	if busy == 0 {
		busy = 2 * time.Second
	}
	watch := m.Pub.Watch(UsageTopic)
	every := idle
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			next := idle
			if m.Pub.Subscribers(UsageTopic) > 0 {
				next = busy
			}
			if next != every {
				every = next
				t.Reset(0) // a new subscriber gets a reading at once
			}
		case <-t.C:
			m.sample(ctx, time.Now())
			t.Reset(every)
		}
	}
}

func (m *Monitor) installed() bool {
	sock := m.Socket
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	_, err := os.Stat(sock)
	return err == nil
}

// sample reads the cgroups, refreshes the list if the running set changed,
// and reports.
func (m *Monitor) sample(ctx context.Context, now time.Time) {
	if !m.installed() {
		m.publish(UsageMsg{Running: []Usage{}, Unexpected: []string{}, Time: now.UnixMilli()})
		return
	}
	found := m.findScopes()
	refresh := m.Refresh
	if refresh == 0 {
		refresh = 5 * time.Minute
	}
	m.mu.Lock()
	changed := len(found) != len(m.scopes) || now.Sub(m.read) >= refresh
	for id, s := range found {
		if old, ok := m.scopes[id]; !ok || old.ino != s.ino {
			changed = true
		} else {
			s.usec, s.taken = old.usec, old.taken // keep the previous reading for the rate
			found[id] = s
		}
	}
	// After a failure (Docker stopped, no broker) wait for Refresh rather
	// than wake the broker on every reading.
	retry := m.known || m.read.IsZero()
	m.mu.Unlock()
	if changed && (retry || now.Sub(m.read) >= refresh) {
		ok := m.refresh(ctx)
		m.mu.Lock()
		m.read = now
		m.known = m.known || ok
		m.mu.Unlock()
	}

	m.mu.Lock()
	running := make([]Usage, 0, len(found))
	for id, s := range found {
		u := Usage{ID: id, Name: m.names[id]}
		if u.Name == "" {
			u.Name = id[:12]
		}
		if usec, ok := cpuUsec(s.dir); ok {
			if !s.taken.IsZero() && usec >= s.usec && now.After(s.taken) {
				u.CPU = float64(usec-s.usec) / float64(now.Sub(s.taken).Microseconds()) * 100
			}
			s.usec, s.taken = usec, now
		}
		u.Mem = memUsed(s.dir)
		found[id] = s
		running = append(running, u)
	}
	m.scopes = found
	sort.Slice(running, func(i, j int) bool {
		if running[i].CPU != running[j].CPU {
			return running[i].CPU > running[j].CPU
		}
		return running[i].Name < running[j].Name
	})
	msg := UsageMsg{Available: true, Running: running, Stopped: m.stopped, Unexpected: append([]string{}, m.unexp...), Time: now.UnixMilli()}
	known := m.known
	m.mu.Unlock()
	m.publish(msg)
	if known && m.Observe != nil {
		values := make(map[string]float64, len(msg.Unexpected))
		for _, n := range msg.Unexpected {
			values[n] = 1
		}
		m.Observe(ctx, now, values)
	}
}

func (m *Monitor) publish(msg UsageMsg) {
	m.mu.Lock()
	m.last = msg
	m.mu.Unlock()
	if m.Pub.Subscribers(UsageTopic) > 0 {
		_ = m.Pub.Publish(UsageTopic, msg)
	}
}

// listed is the part of docker.containers.list the monitor reads.
type listed struct {
	ID     string `json:"Id"`
	Names  []string
	State  string
	Status string
}

// refresh reads every container's name and state through the broker.
func (m *Monitor) refresh(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var list []listed
	if err := m.B.Call(cctx, "docker.containers.list", map[string]bool{"all": true}, &list); err != nil {
		if m.Log != nil {
			m.Log.Debug("docker monitor: list", "err", err)
		}
		return false
	}
	names := make(map[string]string, len(list))
	stopped := 0
	var unexp []string
	for _, c := range list {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		names[c.ID] = name
		if c.State != "running" && c.State != "paused" && c.State != "restarting" {
			stopped++
		}
		if m.unexpected(cctx, c) {
			unexp = append(unexp, name)
		}
	}
	sort.Strings(unexp)
	m.mu.Lock()
	m.names, m.stopped, m.unexp = names, stopped, unexp
	m.mu.Unlock()
	return true
}

var exitCode = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// unexpected reports whether a container stopped on its own: it is dead,
// in a restart loop, or exited with a failure code. Codes from being
// stopped (130 SIGINT, 143 SIGTERM, 137 SIGKILL after the stop timeout)
// are expected, except 137 from the kernel's OOM killer.
func (m *Monitor) unexpected(ctx context.Context, c listed) bool {
	switch c.State {
	case "dead", "restarting":
		return true
	case "exited":
	default:
		return false
	}
	sm := exitCode.FindStringSubmatch(c.Status)
	if sm == nil {
		return false
	}
	switch code, _ := strconv.Atoi(sm[1]); code {
	case 0, 130, 143:
		return false
	case 137:
		return m.oomKilled(ctx, c.ID)
	}
	return true
}

func (m *Monitor) oomKilled(ctx context.Context, id string) bool {
	var r struct {
		Inspect struct {
			State struct{ OOMKilled bool }
		} `json:"inspect"`
	}
	if err := m.B.Call(ctx, "docker.containers.inspect", map[string]string{"id": id}, &r); err != nil {
		return false
	}
	return r.Inspect.State.OOMKilled
}

var scopeName = regexp.MustCompile(`^(?:docker-)?([0-9a-f]{64})(?:\.scope)?$`)

// findScopes lists running containers' cgroups: systemd's
// system.slice/docker-<id>.scope, or cgroupfs's docker/<id>.
func (m *Monitor) findScopes() map[string]scope {
	root := m.CgroupRoot
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	out := map[string]scope{}
	for _, parent := range []string{filepath.Join(root, "system.slice"), filepath.Join(root, "docker")} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			sm := scopeName.FindStringSubmatch(e.Name())
			if sm == nil || !e.IsDir() || (parent == filepath.Join(root, "system.slice")) != strings.HasPrefix(e.Name(), "docker-") {
				continue
			}
			dir := filepath.Join(parent, e.Name())
			var ino uint64
			if fi, err := os.Stat(dir); err == nil {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok {
					ino = st.Ino
				}
			}
			out[sm[1]] = scope{dir: dir, ino: ino}
		}
	}
	return out
}

// cpuUsec reads usage_usec from a cgroup's cpu.stat.
func cpuUsec(dir string) (uint64, bool) {
	v, ok := statField(filepath.Join(dir, "cpu.stat"), "usage_usec")
	return v, ok
}

// memUsed is memory.current minus inactive_file, as docker stats counts it.
func memUsed(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "memory.current")) // #nosec G304 -- under the cgroup root
	if err != nil {
		return 0
	}
	cur, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	if inactive, ok := statField(filepath.Join(dir, "memory.stat"), "inactive_file"); ok && inactive < cur {
		return cur - inactive
	}
	return cur
}

func statField(path, key string) (uint64, bool) {
	f, err := os.Open(path) // #nosec G304 -- under the cgroup root
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == key {
			n, err := strconv.ParseUint(v, 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// HandleUsage serves GET /api/v1/docker/usage: the latest reading.
func (m *Monitor) HandleUsage(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	msg := m.last
	m.mu.Unlock()
	if msg.Time == 0 {
		msg = UsageMsg{Available: m.installed(), Running: []Usage{}, Unexpected: []string{}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(msg)
}
