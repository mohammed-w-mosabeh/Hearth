// Package procctl implements the process actions (SPEC 8): terminate,
// kill, freeze, resume and renice, for one process or a whole group from
// the Apps view. hearthd runs as the user, so only the user's own
// processes can be acted on; system services are stopped through systemd
// (ROADMAP 1.13).
package procctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

// Reasons an item is blocked or failed; the UI translates them.
const (
	ReasonOtherUser = "other-user" // owned by another user (Detail: user)
	ReasonSystem    = "system"     // PID 1, the user manager, kernel threads
	ReasonSelf      = "self"       // hearthd itself
	ReasonSession   = "session"    // display server / shell: would end or freeze the session
	ReasonBrowser   = "browser"    // the browser showing Hearth: freezing it freezes this page
	ReasonGone      = "gone"       // exited meanwhile
	ReasonDenied    = "denied"     // the kernel refused (e.g. raising priority)
	ReasonError     = "error"
)

// sessionCritical are processes whose end (or freezing) ends or locks the
// graphical session.
var sessionCritical = map[string]bool{
	"gnome-shell": true, "gnome-session-binary": true, "gnome-session-service": true, "gnome-session-ctl": true,
	"Xwayland": true, "Xorg": true, "X": true, "mutter": true,
	"kwin_wayland": true, "kwin_x11": true, "plasmashell": true, "ksmserver": true, "startplasma-wayland": true,
	"sway": true, "Hyprland": true, "weston": true, "labwc": true, "wayfire": true, "cosmic-comp": true,
	"xfce4-session": true, "xfwm4": true, "lxqt-session": true, "cinnamon": true, "cinnamon-session": true,
	"systemd": true, "dbus-daemon": true, "dbus-broker": true, "pipewire": true, "wireplumber": true,
}

// Config configures the process actions.
type Config struct {
	ProcRoot   string // default /proc
	CgroupRoot string // default /sys/fs/cgroup
	// AppGroups, if set, enables app/close: it returns the process
	// groups ("app:<unit app ID>") an application runs in now and its
	// name, or ok false for an unknown app.
	AppGroups func(appID string) (groups []string, name string, ok bool)
}

type ctl struct {
	cfg  Config
	self int
}

func (c *ctl) sampler() *collect.ProcSampler {
	return collect.NewProcSampler(collect.ProcConfig{ProcRoot: c.cfg.ProcRoot, CgroupRoot: c.cfg.CgroupRoot})
}

var (
	groupTarget = regexp.MustCompile(`^group:([a-z-]+:?[A-Za-z0-9._@:/ \\-]{0,300}|kernel)$`)
	pidTarget   = regexp.MustCompile(`^pid:([1-9][0-9]{0,9}):([0-9]{1,20})$`)
)

// target parses "group:<id>" or "pid:<pid>:<start>".
func target(s string) (group string, pid int, start uint64, err error) {
	if m := pidTarget.FindStringSubmatch(s); m != nil {
		pid, err1 := strconv.Atoi(m[1])
		start, err2 := strconv.ParseUint(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			return "", 0, 0, errors.New("bad pid target")
		}
		return "", pid, start, nil
	}
	if m := groupTarget.FindStringSubmatch(s); m != nil {
		return m[1], 0, 0, nil
	}
	return "", 0, 0, errors.New(`target must be "group:<id>" or "pid:<pid>:<start>"`)
}

func validate(r actions.Request) error {
	_, _, _, err := target(r.Target)
	return err
}

// resolve returns the processes the target names now.
func (c *ctl) resolve(t string) ([]collect.ProcRef, error) {
	group, pid, start, err := target(t)
	if err != nil {
		return nil, actions.Errorf(http.StatusBadRequest, "%v", err)
	}
	refs, err := c.sampler().Lookup(group, pid, start)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, actions.Errorf(http.StatusNotFound, "no longer running")
	}
	return refs, nil
}

// sessionSlice holds the units that make up the graphical session (GNOME:
// org.gnome.Shell@wayland.service with Xwayland, gnome-session-manager...).
const sessionSlice = "/session.slice/"

// check sorts refs into what action may touch and what it must not.
func (c *ctl) check(action string, refs []collect.ProcRef, browserGroup string) ([]actions.Item, []actions.Blocked) {
	// A group with a session-critical member is session-critical as a
	// whole: its other members (Xwayland, frame drawers) belong to it.
	critical := map[string]bool{}
	for _, r := range refs {
		if sessionCritical[r.Name] || strings.Contains(r.Cgroup, sessionSlice) {
			critical[r.Group] = true
		}
	}
	var ok []actions.Item
	var blocked []actions.Blocked
	for _, r := range refs {
		it := actions.Item{ID: fmt.Sprintf("pid:%d:%d", r.PID, r.Start), Name: r.Name, Detail: r.User}
		reason := ""
		switch {
		case r.PID == 1 || r.Kind == collect.KindKernel || r.Group == "unit:init.scope" || r.Group == "user-unit:init.scope":
			reason = ReasonSystem
		case r.PID == c.self:
			reason = ReasonSelf
		case !r.Own:
			reason = ReasonOtherUser
		case action != "renice" && action != "resume" && (sessionCritical[r.Name] || critical[r.Group]):
			reason = ReasonSession
		case action == "freeze" && browserGroup != "" && r.Group == browserGroup:
			reason = ReasonBrowser
		}
		if reason != "" {
			blocked = append(blocked, actions.Blocked{Item: it, Reason: reason})
		} else {
			ok = append(ok, it)
		}
	}
	return ok, blocked
}

// Register adds the proc/* actions to reg.
func Register(reg *actions.Registry, cfg Config) {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	c := &ctl{cfg: cfg, self: os.Getpid()}

	plan := func(action string) func(ctx context.Context, r actions.Request) (actions.Plan, error) {
		return func(_ context.Context, r actions.Request) (actions.Plan, error) {
			refs, err := c.resolve(r.Target)
			if err != nil {
				return actions.Plan{}, err
			}
			browser := ""
			if action == "freeze" {
				browser = c.browserGroup(r.Client)
			}
			items, blocked := c.check(action, refs, browser)
			p := actions.Plan{Summary: fmt.Sprintf("%s %d process(es)", action, len(items)), Items: items, Blocked: blocked}
			if p.Items == nil {
				p.Items = []actions.Item{}
			}
			if p.Blocked == nil {
				p.Blocked = []actions.Blocked{}
			}
			// Killing a whole group outright is high impact: type its name.
			if action == "kill" && strings.HasPrefix(r.Target, "group:") && len(items) > 1 {
				p.Confirm = refs[0].GroupName
			}
			return p, nil
		}
	}
	signal := func(sig syscall.Signal) func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
		return func(_ context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			return c.each(p, func(pid int, start uint64) error { return c.signal(pid, start, sig) }), nil
		}
	}

	reg.Register(actions.Action{Kind: "proc", Name: "terminate", Risk: actions.Destructive,
		Validate: validate, Plan: plan("terminate"), Run: signal(syscall.SIGTERM)})
	reg.Register(actions.Action{Kind: "proc", Name: "kill", Risk: actions.Destructive,
		Validate: validate, Plan: plan("kill"), Run: signal(syscall.SIGKILL)})
	reg.Register(actions.Action{Kind: "proc", Name: "freeze", Risk: actions.Reversible,
		Validate: validate, Plan: plan("freeze"), Run: signal(syscall.SIGSTOP)})
	reg.Register(actions.Action{Kind: "proc", Name: "resume", Risk: actions.Safe,
		Validate: validate, Plan: plan("resume"), Run: signal(syscall.SIGCONT)})
	reg.Register(actions.Action{Kind: "proc", Name: "renice", Risk: actions.Reversible,
		Validate: func(r actions.Request) error {
			if err := validate(r); err != nil {
				return err
			}
			if _, err := nice(r.Params); err != nil {
				return err
			}
			return nil
		},
		Plan: plan("renice"),
		Run: func(_ context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			n, _ := nice(r.Params)
			return c.each(p, func(pid int, start uint64) error { return c.renice(pid, start, n) }), nil
		}})
	if cfg.AppGroups != nil {
		reg.Register(c.closeApp())
	}
}

// closeApp is app/close: end every process of an application, wherever
// it was started from (Hearth, the desktop, Flatpak), with the same
// checks as proc/terminate.
func (c *ctl) closeApp() actions.Action {
	return actions.Action{Kind: "app", Name: "close", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if !discovery.ValidID(r.Target) || len(r.Params) != 0 {
				return errors.New("target must be an application ID and params empty")
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			groups, name, ok := c.cfg.AppGroups(r.Target)
			if !ok {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "unknown application")
			}
			var refs []collect.ProcRef
			if len(groups) > 0 {
				var err error
				if refs, err = c.sampler().LookupGroups(groups); err != nil {
					return actions.Plan{}, err
				}
			}
			if len(refs) == 0 {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "%s is not running", name)
			}
			items, blocked := c.check("terminate", refs, "")
			p := actions.Plan{Summary: fmt.Sprintf("close %s: %d process(es)", name, len(items)), Items: items, Blocked: blocked}
			if p.Items == nil {
				p.Items = []actions.Item{}
			}
			if p.Blocked == nil {
				p.Blocked = []actions.Blocked{}
			}
			return p, nil
		},
		Run: func(_ context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			return c.each(p, func(pid int, start uint64) error { return c.signal(pid, start, syscall.SIGTERM) }), nil
		}}
}

func nice(p map[string]string) (int, error) {
	n, err := strconv.Atoi(p["nice"])
	if err != nil || n < -20 || n > 19 || len(p) != 1 {
		return 0, errors.New(`params must be {"nice": "-20".."19"}`)
	}
	return n, nil
}

// each applies fn to every planned item.
func (c *ctl) each(p actions.Plan, fn func(pid int, start uint64) error) actions.Result {
	res := actions.Result{Failed: []actions.Blocked{}}
	for _, it := range p.Items {
		_, pid, start, err := target(it.ID)
		if err == nil {
			err = fn(pid, start)
		}
		switch {
		case err == nil:
			res.Done++
		case errors.Is(err, os.ErrProcessDone) || errors.Is(err, errGone):
			res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonGone})
		case errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES):
			res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonDenied})
		default:
			res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
		}
	}
	return res
}

var errGone = errors.New("process exited")

// signal sends sig through a pidfd (os.FindProcess opens one on Linux), so
// once the start time is checked the signal cannot reach a different
// process that reused the pid.
func (c *ctl) signal(pid int, start uint64, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return errGone
	}
	defer func() { _ = p.Release() }()
	if got, err := c.sampler().StartTime(pid); err != nil || got != start {
		return errGone
	}
	return p.Signal(sig)
}

// renice sets the nice value. setpriority has no pidfd form; the start
// time is checked immediately before. Lowering nice needs privileges and
// fails with EACCES, reported as denied.
func (c *ctl) renice(pid int, start uint64, n int) error {
	if got, err := c.sampler().StartTime(pid); err != nil || got != start {
		return errGone
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, n)
}

// browserGroup returns the process group of the browser behind the
// request's TCP connection (client is RemoteAddr, "127.0.0.1:port"), or "".
// hearthd listens on IPv4 loopback only, so the client's socket is in
// /proc/net/tcp with local address 127.0.0.1:port.
func (c *ctl) browserGroup(client string) string {
	host, port, err := net.SplitHostPort(client)
	if err != nil || host != "127.0.0.1" {
		return ""
	}
	pn, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return ""
	}
	inode := socketInode(filepath.Join(c.cfg.ProcRoot, "net/tcp"), fmt.Sprintf("0100007F:%04X", pn))
	if inode == "" {
		return ""
	}
	pid := c.socketOwner(inode)
	if pid == 0 {
		return ""
	}
	s := c.sampler()
	start, err := s.StartTime(pid)
	if err != nil {
		return ""
	}
	refs, err := s.Lookup("", pid, start)
	if err != nil || len(refs) == 0 {
		return ""
	}
	return refs[0].Group
}

func socketInode(path, local string) string {
	f, err := os.Open(path) // #nosec G304 -- fixed /proc path
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		if len(fl) >= 10 && fl[1] == local {
			return fl[9]
		}
	}
	return ""
}

// socketOwner finds the user's process holding socket inode.
func (c *ctl) socketOwner(inode string) int {
	want := "socket:[" + inode + "]"
	entries, err := os.ReadDir(c.cfg.ProcRoot)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == c.self {
			continue
		}
		fdDir := filepath.Join(c.cfg.ProcRoot, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // another user's process
		}
		for _, fd := range fds {
			if l, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && l == want {
				return pid
			}
		}
	}
	return 0
}
