package netinfo

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
)

// Owner is what a socket belongs to.
type Owner struct {
	// The process: exact for the user's own processes (its fd holds the
	// socket); otherwise the first process of the socket's cgroup, which
	// for a service is normally its main process (Exact false).
	PID   int    `json:"pid,omitempty"`
	Start uint64 `json:"start,omitempty"` // with PID, identifies the process
	Name  string `json:"name,omitempty"`  // its comm
	Exact bool   `json:"exact,omitempty"`
	// The process group as on the Processes page ("unit:cups.service",
	// "container:0123456789ab", "app:org.gnome.Epiphany", "exe:nc").
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Container string `json:"container,omitempty"` // 12-hex id for docker-<id>.scope
}

// Resolver finds socket owners.
type Resolver struct {
	ProcRoot   string // default /proc
	CgroupRoot string // default /sys/fs/cgroup
	// AppName, if set, names app groups (discovery.Index.Name).
	AppName func(appID string) (string, bool)

	mu      sync.Mutex
	cgPaths map[uint64]string // cgroup id -> path below the root ("/system.slice/x.service")
}

func (r *Resolver) procRoot() string {
	if r.ProcRoot == "" {
		return "/proc"
	}
	return r.ProcRoot
}

func (r *Resolver) cgroupRoot() string {
	if r.CgroupRoot == "" {
		return "/sys/fs/cgroup"
	}
	return r.CgroupRoot
}

// procRef is a process holding a socket.
type procRef struct {
	pid   int
	start uint64
	name  string
	cg    string
}

// fdOwners maps the socket inodes in want to the processes holding them,
// for the processes whose fd directory is readable (the user's own).
func (r *Resolver) fdOwners(want map[uint32]bool) map[uint32]procRef {
	out := map[uint32]procRef{}
	if len(want) == 0 {
		return out
	}
	root := r.procRoot()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fdDir := filepath.Join(root, d.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // another user's process
		}
		var ref *procRef
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			s, ok := strings.CutPrefix(link, "socket:[")
			if !ok {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(s, "]"), 10, 32)
			if err != nil || !want[uint32(ino)] {
				continue
			}
			if _, seen := out[uint32(ino)]; seen {
				continue // a forked child sharing it; the parent (lower pid) wins
			}
			if ref == nil {
				p := r.proc(pid)
				ref = &p
			}
			out[uint32(ino)] = *ref
		}
	}
	return out
}

// proc reads a process's name, start time and cgroup.
func (r *Resolver) proc(pid int) procRef {
	dir := filepath.Join(r.procRoot(), strconv.Itoa(pid))
	p := procRef{pid: pid}
	if b, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil { // #nosec G304 -- numeric pid under /proc
		p.name, p.start = parseStat(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil { // #nosec G304 -- numeric pid under /proc
		for _, line := range strings.Split(string(b), "\n") {
			if cg, ok := strings.CutPrefix(line, "0::"); ok {
				p.cg = cg
			}
		}
	}
	return p
}

// parseStat reads comm and starttime (field 22) from /proc/<pid>/stat.
func parseStat(b []byte) (name string, start uint64) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return "", 0
	}
	name = string(b[open+1 : end])
	f := strings.Fields(string(b[end+1:]))
	if len(f) > 19 { // state is field 3, starttime field 22
		start, _ = strconv.ParseUint(f[19], 10, 64)
	}
	return name, start
}

// maxCgroups bounds the cgroup walk.
const maxCgroups = 10000

// cgroupPath returns the path of cgroup id, walking the hierarchy again
// when id is not known yet (a new unit since the last walk).
func (r *Resolver) cgroupPath(id uint64, walked *bool) string {
	if id == 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.cgPaths[id]; ok {
		return p
	}
	if *walked {
		return ""
	}
	*walked = true
	r.cgPaths = map[uint64]string{}
	root := r.cgroupRoot()
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		if len(r.cgPaths) >= maxCgroups || depth > 10 {
			return
		}
		var st syscall.Stat_t
		if err := syscall.Stat(root+rel, &st); err != nil {
			return
		}
		r.cgPaths[st.Ino] = rel
		entries, err := os.ReadDir(root + rel)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(strings.TrimSuffix(rel, "/")+"/"+e.Name(), depth+1)
			}
		}
	}
	walk("/", 0)
	return r.cgPaths[id]
}

// firstProc is the lowest pid in a cgroup (its cgroup.procs is readable
// by everyone), or 0.
func (r *Resolver) firstProc(cg string) int {
	b, err := os.ReadFile(filepath.Join(r.cgroupRoot(), filepath.Clean("/"+cg), "cgroup.procs")) // #nosec G304 -- a path from the cgroup walk, under the cgroup root
	if err != nil {
		return 0
	}
	low := 0
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 && (low == 0 || n < low) {
			low = n
		}
	}
	return low
}

// owner builds a socket's Owner from its holding process (if known) or
// its cgroup.
func (r *Resolver) owner(s Socket, fd map[uint32]procRef, walked *bool, procs map[int]procRef) *Owner {
	p, exact := fd[s.Inode]
	cg := p.cg
	if !exact {
		cg = r.cgroupPath(s.Cgroup, walked)
		if cg == "" {
			return nil
		}
		if pid := r.firstProc(cg); pid > 0 {
			if cached, ok := procs[pid]; ok {
				p = cached
			} else {
				p = r.proc(pid)
				procs[pid] = p
			}
			p.cg = cg
		}
	}
	o := &Owner{PID: p.pid, Start: p.start, Name: p.name, Exact: exact}
	o.Group, o.Kind, o.GroupName, o.Unit = collect.GroupOf(cg, p.name)
	if o.Kind == collect.KindApp && r.AppName != nil {
		if n, ok := r.AppName(strings.TrimPrefix(o.Group, "app:")); ok {
			o.GroupName = n
		}
	}
	if o.Kind == collect.KindContainer {
		o.Container = strings.TrimPrefix(o.Group, "container:")
	}
	if o.Group == "exe:" { // no process known: say nothing rather than a blank group
		o.Group, o.Kind, o.GroupName = "", "", ""
	}
	return o
}
