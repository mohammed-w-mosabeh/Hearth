package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProcTopic is the WebSocket topic process groups are published on.
const ProcTopic = "procs"

// ProcInterval is how often processes are scanned while someone watches.
// A scan reads one file per process plus two per cgroup, so it runs only on
// demand.
const ProcInterval = 2 * time.Second

// maxGroupProcs bounds the per-group process list sent to the UI.
const maxGroupProcs = 20

// Group kinds.
const (
	KindApp         = "app"          // desktop app scope (app-*.scope)
	KindService     = "service"      // system unit
	KindUserService = "user-service" // unit of the user manager
	KindContainer   = "container"    // docker-<id>.scope
	KindSession     = "session"      // login session, grouped by executable
	KindKernel      = "kernel"       // kernel threads
)

// Proc is one process in a group.
type Proc struct {
	PID   int     `json:"pid"`
	Start uint64  `json:"start"` // start time in clock ticks since boot; with PID, identifies the process
	Name  string  `json:"name"`
	State string  `json:"state"`
	CPU   float64 `json:"cpu"` // % of the whole machine
	RSS   uint64  `json:"rss"` // resident bytes, shared pages included
}

// Group is the processes of one app, unit, container or executable.
type Group struct {
	ID    string  `json:"id"` // e.g. "app:com.microsoft.VSCode", "unit:cups.service"
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Unit  string  `json:"unit,omitempty"` // systemd unit or scope
	User  string  `json:"user"`           // owner of the first process seen
	Count int     `json:"count"`
	CPU   float64 `json:"cpu"`
	// Mem is the cgroup's memory.current for groups made of whole cgroups
	// (what systemd reports: shared pages counted once, page cache
	// included); otherwise the sum of RSS.
	Mem uint64 `json:"mem"`
	// Disk I/O from the cgroups' io.stat, or, where the io controller is
	// not enabled (user apps: systemd delegates only cpu/memory/pids to
	// the user manager), from /proc/<pid>/io of the user's own processes.
	// nil when unknown and on the first scan; IOPartial when some of the
	// group's processes could not be read (another user's).
	ReadB     *float64 `json:"read_bps,omitempty"`
	WriteB    *float64 `json:"write_bps,omitempty"`
	IOPartial bool     `json:"io_partial,omitempty"`
	Procs     []Proc   `json:"procs"` // busiest first, at most maxGroupProcs
}

// ProcSnapshot is one scan.
type ProcSnapshot struct {
	Time   time.Time `json:"t"`
	Groups []Group   `json:"groups"`
	// Partial is set on the first scan, which has no CPU or I/O deltas.
	Partial bool `json:"partial,omitempty"`
}

// procKey identifies a process across scans (pids are reused).
type procKey struct {
	pid   int
	start uint64
}

// procInfo is what does not change for a process, read once.
type procInfo struct {
	name, user string
	group      groupID
	cgroup     string // unified hierarchy path when its io.stat is used
	// procIO: read /proc/<pid>/io (own process, no cgroup io.stat);
	// noIO: neither source is available.
	procIO, noIO bool
	memCgroup    string // cgroup whose memory.current counts this process
	uid          int    // -1 if unknown
}

type procPrev struct {
	ticks       uint64
	read, write uint64
	ioOK        bool
}

type groupID struct {
	id, kind, name, unit string
	// own is set when the group is made of whole cgroups, so memory and
	// I/O can be read per cgroup.
	own bool
}

type ioBytes struct {
	read, write uint64
}

// ProcConfig configures a ProcSampler.
type ProcConfig struct {
	ProcRoot   string
	CgroupRoot string
	Interval   time.Duration
	Pub        Publisher
	Log        *slog.Logger
	// Users maps uid to user name; nil reads /etc/passwd.
	Users func() map[int]string
	// AppName, if set, names app groups from their .desktop entry
	// (discovery.Index.Name).
	AppName func(appID string) (string, bool)
}

// ProcSampler scans /proc while the procs topic has subscribers.
type ProcSampler struct {
	cfg      ProcConfig
	info     map[procKey]procInfo
	prev     map[procKey]procPrev
	prevIO   map[string]ioBytes // by cgroup path
	prevAt   time.Time
	prevCPU  uint64 // total jiffies of all CPUs
	users    map[int]string
	usersAt  time.Time
	buf      []byte
	pageSize uint64
	uid      int
}

// NewProcSampler returns a ProcSampler; call Run to start it.
func NewProcSampler(cfg ProcConfig) *ProcSampler {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if cfg.Interval == 0 {
		cfg.Interval = ProcInterval
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Users == nil {
		cfg.Users = readPasswd
	}
	p := &ProcSampler{cfg: cfg, pageSize: pageSize(), uid: os.Getuid()}
	p.reset()
	return p
}

// Run scans every Interval while the topic has subscribers and sleeps
// otherwise, dropping its caches so an idle hearthd holds no process state.
func (p *ProcSampler) Run(ctx context.Context) {
	watch := p.cfg.Pub.Watch(ProcTopic)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
			continue
		case <-timer.C:
		}
		if p.cfg.Pub.Subscribers(ProcTopic) == 0 {
			p.reset()
			continue // the watch channel wakes us
		}
		snap, err := p.Scan()
		if err != nil {
			p.cfg.Log.Warn("procs: scan failed", "err", err)
		} else if err := p.cfg.Pub.Publish(ProcTopic, snap); err != nil {
			p.cfg.Log.Warn("procs: publish failed", "err", err)
		}
		timer.Reset(p.cfg.Interval)
	}
}

func (p *ProcSampler) reset() {
	p.info = map[procKey]procInfo{}
	p.prev = map[procKey]procPrev{}
	p.prevIO = map[string]ioBytes{}
	p.prevAt, p.prevCPU = time.Time{}, 0
	p.buf = nil
}

// read reads ProcRoot/rel into the shared scratch buffer (valid until the
// next read). Per-pid paths are not cached: pids come and go.
func (p *ProcSampler) read(rel string) ([]byte, error) {
	return p.readAt(p.cfg.ProcRoot, rel)
}

func (p *ProcSampler) readAt(root, rel string) ([]byte, error) {
	f, err := os.Open(filepath.Join(root, rel)) // #nosec G304 -- fixed names under configured roots; cgroup paths come from /proc/<pid>/cgroup, cleaned
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := readInto(f, p.buf[:0])
	if err != nil {
		return nil, err
	}
	p.buf = b
	return b, nil
}

// Scan reads all processes once. CPU and I/O rates are relative to the
// previous Scan.
func (p *ProcSampler) Scan() (ProcSnapshot, error) {
	now := time.Now()
	if p.users == nil || now.Sub(p.usersAt) > time.Minute {
		p.users, p.usersAt = p.cfg.Users(), now
	}
	b, err := p.read("stat")
	if err != nil {
		return ProcSnapshot{}, fmt.Errorf("procs: %w", err)
	}
	cpuTotal, err := totalJiffies(b)
	if err != nil {
		return ProcSnapshot{}, err
	}
	entries, err := os.ReadDir(p.cfg.ProcRoot)
	if err != nil {
		return ProcSnapshot{}, fmt.Errorf("procs: %w", err)
	}

	first := p.prevAt.IsZero()
	dt := now.Sub(p.prevAt).Seconds()
	dCPU := float64(cpuTotal - p.prevCPU)
	groups := map[string]*Group{}
	memCg := map[string]map[string]bool{} // group id -> cgroups for memory
	ioCg := map[string]map[string]bool{}  // group id -> cgroups for io.stat
	type sum struct {
		read, write  float64
		any, partial bool
	}
	procIO := map[string]*sum{}
	seen := make(map[procKey]procPrev, len(p.prev))
	seenInfo := make(map[procKey]procInfo, len(p.info))

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		st, err := p.readStat(pid)
		if err != nil {
			continue // exited during the scan
		}
		key := procKey{pid, st.start}
		info, ok := p.info[key]
		if !ok {
			info = p.readInfo(pid, st)
		}
		seenInfo[key] = info
		cur := procPrev{ticks: st.ticks}
		if info.procIO {
			if b, err := p.read(strconv.Itoa(pid) + "/io"); err == nil {
				cur.read, cur.write, cur.ioOK = parseProcIO(b)
			}
		}
		seen[key] = cur

		pr := Proc{PID: pid, Start: st.start, Name: info.name, State: st.state, RSS: st.rss * p.pageSize}
		old, had := p.prev[key]
		if had && !first && dCPU > 0 && st.ticks >= old.ticks {
			pr.CPU = float64(st.ticks-old.ticks) / dCPU * 100
		}

		g := groups[info.group.id]
		if g == nil {
			g = &Group{ID: info.group.id, Kind: info.group.kind, Name: info.group.name, Unit: info.group.unit, User: info.user}
			groups[info.group.id] = g
		}
		g.Count++
		g.CPU += pr.CPU
		if info.memCgroup != "" {
			addSet(memCg, g.ID, info.memCgroup)
		} else {
			g.Mem += pr.RSS
		}
		switch {
		case info.cgroup != "":
			addSet(ioCg, g.ID, info.cgroup)
		case info.procIO:
			s := procIO[g.ID]
			if s == nil {
				s = &sum{}
				procIO[g.ID] = s
			}
			if cur.ioOK && had && old.ioOK && !first && dt > 0 && cur.read >= old.read && cur.write >= old.write {
				s.read += float64(cur.read-old.read) / dt
				s.write += float64(cur.write-old.write) / dt
				s.any = true
			} else if !first {
				s.partial = true
			}
		case info.noIO && info.group.kind != KindKernel:
			if s := procIO[g.ID]; s != nil {
				s.partial = true
			} else {
				procIO[g.ID] = &sum{partial: true}
			}
		}
		g.Procs = append(g.Procs, pr)
	}
	for id, s := range procIO {
		g := groups[id]
		if s.any {
			g.ReadB, g.WriteB = &s.read, &s.write
		}
		g.IOPartial = s.partial && s.any
	}

	// Memory and I/O of whole cgroups: one read per cgroup rather than one
	// per process, and readable for every user's cgroups.
	for id, paths := range memCg {
		g := groups[id]
		for cg := range paths {
			if b, err := p.readAt(p.cfg.CgroupRoot, cg+"/memory.current"); err == nil {
				if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
					g.Mem += n
				}
			}
		}
	}
	seenIO := make(map[string]ioBytes, len(p.prevIO))
	for id, paths := range ioCg {
		g := groups[id]
		var rd, wr float64
		ioOK := !first && dt > 0
		for cg := range paths {
			b, err := p.readAt(p.cfg.CgroupRoot, cg+"/io.stat")
			if err != nil {
				ioOK = false
				continue
			}
			cur := parseIOStat(b)
			seenIO[cg] = cur
			old, ok := p.prevIO[cg]
			if !ok || cur.read < old.read || cur.write < old.write {
				ioOK = false
				continue
			}
			rd += float64(cur.read-old.read) / dt
			wr += float64(cur.write-old.write) / dt
		}
		if ioOK {
			g.ReadB, g.WriteB = &rd, &wr
		}
	}
	p.prev, p.info, p.prevIO, p.prevAt, p.prevCPU = seen, seenInfo, seenIO, now, cpuTotal

	snap := ProcSnapshot{Time: now, Partial: first, Groups: make([]Group, 0, len(groups))}
	for _, g := range groups {
		sort.Slice(g.Procs, func(i, j int) bool {
			if g.Procs[i].CPU != g.Procs[j].CPU {
				return g.Procs[i].CPU > g.Procs[j].CPU
			}
			return g.Procs[i].RSS > g.Procs[j].RSS
		})
		if len(g.Procs) > maxGroupProcs {
			g.Procs = g.Procs[:maxGroupProcs]
		}
		snap.Groups = append(snap.Groups, *g)
	}
	sort.Slice(snap.Groups, func(i, j int) bool {
		a, b := snap.Groups[i], snap.Groups[j]
		if a.CPU != b.CPU {
			return a.CPU > b.CPU
		}
		if a.Mem != b.Mem {
			return a.Mem > b.Mem
		}
		return a.ID < b.ID
	})
	return snap, nil
}

type procStat struct {
	comm   string
	state  string
	ppid   int
	ticks  uint64 // utime + stime
	start  uint64
	rss    uint64 // pages
	kernel bool
}

func (p *ProcSampler) readStat(pid int) (procStat, error) {
	b, err := p.read(strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	return parseProcStat(b)
}

// parseProcStat parses /proc/<pid>/stat. comm is in parentheses and may
// itself contain spaces and parentheses, so fields are counted from the
// last ")".
func parseProcStat(b []byte) (procStat, error) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return procStat{}, errors.New("procs: malformed stat")
	}
	f := strings.Fields(string(b[end+1:]))
	// f[0] is field 3 (state); flags=9, utime=14, stime=15, starttime=22,
	// rss=24.
	if len(f) < 22 {
		return procStat{}, errors.New("procs: short stat")
	}
	st := procStat{comm: string(b[open+1 : end]), state: f[0]}
	st.ppid, _ = strconv.Atoi(f[1])
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	st.ticks = ut + stime
	st.start, _ = strconv.ParseUint(f[19], 10, 64)
	st.rss, _ = strconv.ParseUint(f[21], 10, 64)
	flags, _ := strconv.ParseUint(f[6], 10, 64)
	const pfKthread = 0x00200000
	st.kernel = flags&pfKthread != 0
	return st, nil
}

// pageSize is the size of the pages /proc/<pid>/stat counts RSS in.
func pageSize() uint64 {
	if n := os.Getpagesize(); n > 0 {
		return uint64(n)
	}
	return 4096
}

func addSet(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

// parseProcIO returns read_bytes and write_bytes of /proc/<pid>/io
// (storage I/O, not cache hits).
func parseProcIO(b []byte) (read, write uint64, ok bool) {
	var gotR, gotW bool
	for _, line := range strings.Split(string(b), "\n") {
		k, v, found := strings.Cut(line, ": ")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "read_bytes":
			read, gotR = n, true
		case "write_bytes":
			write, gotW = n, true
		}
	}
	return read, write, gotR && gotW
}

// parseIOStat sums rbytes and wbytes over the devices of a cgroup io.stat
// ("8:0 rbytes=1 wbytes=2 rios=3 ...").
func parseIOStat(b []byte) ioBytes {
	var c ioBytes
	for _, kv := range strings.Fields(string(b)) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "rbytes":
			c.read += n
		case "wbytes":
			c.write += n
		}
	}
	return c
}

func totalJiffies(stat []byte) (uint64, error) {
	line, _, _ := bytes.Cut(stat, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return 0, errors.New("procs: malformed /proc/stat")
	}
	var total uint64
	for _, s := range f[1:] {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, errors.New("procs: malformed /proc/stat")
		}
		total += n
	}
	return total, nil
}

// readInfo reads what does not change for a process: its name, owner and
// group. Called once per process lifetime.
func (p *ProcSampler) readInfo(pid int, st procStat) procInfo {
	info := procInfo{name: st.comm}
	if st.kernel || pid == 2 || st.ppid == 2 {
		info.group = groupID{id: "kernel", kind: KindKernel, name: "Kernel"}
		info.user, info.uid = "root", 0
		return info
	}
	if b, err := p.read(strconv.Itoa(pid) + "/cmdline"); err == nil && len(b) > 0 {
		arg0, _, _ := bytes.Cut(b, []byte{0})
		// Some programs rewrite argv[0] to a title with spaces; keep
		// comm for those.
		if base := filepath.Base(string(arg0)); base != "." && base != "/" && !strings.ContainsAny(base, " :") {
			info.name = base
		}
	}
	own := false
	info.uid = -1
	if b, err := p.read(strconv.Itoa(pid) + "/status"); err == nil {
		if uid, ok := statusUID(b); ok {
			info.uid = uid
			info.user = p.users[uid]
			if info.user == "" {
				info.user = strconv.Itoa(uid)
			}
			own = uid == p.uid
		}
	}
	var cg string
	if b, err := p.read(strconv.Itoa(pid) + "/cgroup"); err == nil {
		cg = cgroupV2Path(b)
	}
	info.group = groupFor(cg, info.name)
	if info.group.kind == KindApp && p.cfg.AppName != nil {
		if n, ok := p.cfg.AppName(strings.TrimPrefix(info.group.id, "app:")); ok {
			info.group.name = n
		}
	}
	if info.group.own {
		path := filepath.Clean("/" + cg)
		info.memCgroup = path
		// Controllers are fixed for a cgroup's life, so this is checked
		// once per process.
		if _, err := os.Stat(filepath.Join(p.cfg.CgroupRoot, path, "io.stat")); err == nil {
			info.cgroup = path
		}
	}
	if info.cgroup == "" {
		info.procIO, info.noIO = own, !own
	}
	return info
}

func statusUID(b []byte) (int, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				uid, err := strconv.Atoi(f[0])
				return uid, err == nil
			}
		}
	}
	return 0, false
}

// cgroupV2Path returns the unified hierarchy path ("0::/...").
func cgroupV2Path(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p
		}
	}
	return ""
}

// launchers are prefixes desktops put before the app ID in scope names
// (app-gnome-org.gnome.TextEditor-1234.scope).
var launchers = []string{"gnome-", "kde-", "flatpak-", "snap-", "xfce-", "cosmic-"}

// groupFor maps a cgroup path to a group, following systemd's desktop
// naming (systemd.special(7), "app-[<launcher>-]<ApplicationID>-<RANDOM>.scope"
// and "app-[<launcher>-]<ApplicationID>[@<RANDOM>].service").
func groupFor(cgroup, exe string) groupID {
	unit := filepath.Base(cgroup)
	user := strings.Contains(cgroup, "/user@")
	switch {
	case cgroup == "" || cgroup == "/":
		return groupID{id: "exe:" + exe, kind: KindSession, name: exe}
	case strings.HasPrefix(unit, "hearth-app-") && strings.HasSuffix(unit, ".scope"):
		// Launched by Hearth (ROADMAP 1.11): hearth-app-<id>-<n>.scope
		id := strings.TrimSuffix(strings.TrimPrefix(unit, "hearth-app-"), ".scope")
		if i := strings.LastIndexByte(id, '-'); i > 0 {
			id = id[:i]
		}
		id = unescapeUnit(id)
		return groupID{id: "app:" + id, kind: KindApp, name: appName(id), unit: unit, own: true}
	case strings.HasPrefix(unit, "app-") && (strings.HasSuffix(unit, ".scope") || strings.HasSuffix(unit, ".service")):
		id := strings.TrimPrefix(unit, "app-")
		if strings.HasSuffix(id, ".scope") {
			id = strings.TrimSuffix(id, ".scope")
			if i := strings.LastIndexByte(id, '-'); i > 0 {
				id = id[:i] // -<RANDOM>
			}
		} else {
			id = strings.TrimSuffix(id, ".service")
			if i := strings.IndexByte(id, '@'); i > 0 {
				id = id[:i]
			}
		}
		for _, l := range launchers {
			if rest, ok := strings.CutPrefix(id, l); ok && rest != "" {
				id = rest
				break
			}
		}
		id = unescapeUnit(id)
		return groupID{id: "app:" + id, kind: KindApp, name: appName(id), unit: unit, own: true}
	case strings.HasPrefix(unit, "docker-") && strings.HasSuffix(unit, ".scope"):
		cid := strings.TrimSuffix(strings.TrimPrefix(unit, "docker-"), ".scope")
		if len(cid) > 12 {
			cid = cid[:12]
		}
		return groupID{id: "container:" + cid, kind: KindContainer, name: "Container " + cid, unit: unit, own: true}
	case strings.HasSuffix(unit, ".service"):
		kind, prefix := KindService, "unit:"
		if user {
			kind, prefix = KindUserService, "user-unit:"
		}
		return groupID{id: prefix + unit, kind: kind, name: strings.TrimSuffix(unescapeUnit(unit), ".service"), unit: unit, own: true}
	case unit == "init.scope" && user:
		// The user's own systemd manager, not PID 1.
		return groupID{id: "user-unit:init.scope", kind: KindUserService, name: "systemd --user", unit: unit, own: true}
	case unit == "init.scope":
		return groupID{id: "unit:init.scope", kind: KindService, name: "systemd", unit: unit, own: true}
	default:
		// Login sessions and other scopes: group by executable.
		return groupID{id: "exe:" + exe, kind: KindSession, name: exe}
	}
}

// appName is a readable name from an app ID until .desktop names are known
// (ROADMAP 1.10): "com.microsoft.VSCode" -> "VSCode".
func appName(id string) string {
	if i := strings.LastIndexByte(id, '.'); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}

// unescapeUnit undoes systemd's \xNN escaping in unit names.
func unescapeUnit(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if n, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// readPasswd maps uids to names from /etc/passwd (no cgo, no NSS: users
// from LDAP etc. show as numbers).
func readPasswd() map[int]string {
	users := map[int]string{}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return users
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			users[uid] = f[0]
		}
	}
	return users
}

// ProcRef identifies a process for an action on it.
type ProcRef struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
	Name  string `json:"name"`
	User  string `json:"user"`
	Own   bool   `json:"-"` // owned by hearthd's user
	// Group is the process group id and Kind its kind, as in Group.
	Group string `json:"-"`
	Kind  string `json:"-"`
	// GroupName is the group's display name.
	GroupName string `json:"-"`
	// Cgroup is the process's cgroup path for groups made of whole
	// cgroups ("" otherwise).
	Cgroup string `json:"-"`
}

// Lookup reads the current processes and returns those in group id (when
// pid is 0) or the single process pid with start time start. It is meant
// for one-off use by actions; it does not disturb a running Scan loop as
// long as each caller uses its own ProcSampler.
func (p *ProcSampler) Lookup(id string, pid int, start uint64) ([]ProcRef, error) {
	p.users = p.cfg.Users()
	var pids []int
	if pid > 0 {
		pids = []int{pid}
	} else {
		entries, err := os.ReadDir(p.cfg.ProcRoot)
		if err != nil {
			return nil, fmt.Errorf("procs: %w", err)
		}
		for _, e := range entries {
			if n, err := strconv.Atoi(e.Name()); err == nil && n > 0 {
				pids = append(pids, n)
			}
		}
	}
	var out []ProcRef
	for _, n := range pids {
		st, err := p.readStat(n)
		if err != nil {
			continue
		}
		if pid > 0 && st.start != start {
			continue // the pid now belongs to another process
		}
		info := p.readInfo(n, st)
		if pid == 0 && info.group.id != id {
			continue
		}
		out = append(out, ProcRef{PID: n, Start: st.start, Name: info.name, User: info.user,
			Own: info.uid == p.uid, Group: info.group.id, Kind: info.group.kind, GroupName: info.group.name, Cgroup: info.memCgroup})
	}
	return out, nil
}

// StartTime returns the start time of pid, to check that a pid still names
// the same process.
func (p *ProcSampler) StartTime(pid int) (uint64, error) {
	st, err := p.readStat(pid)
	if err != nil {
		return 0, err
	}
	return st.start, nil
}
