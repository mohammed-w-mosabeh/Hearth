// Package access is the Access section of the Security page (ROADMAP 7.4):
// SSH logins from the journal by source and day, the sessions logind has open,
// and the accounts on this computer with members of sudo and docker flagged.
// It only reads, as the user (SECURITY S16j).
package access

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const (
	logindDest   = "org.freedesktop.login1"
	logindPath   = dbus.ObjectPath("/org/freedesktop/login1")
	listSessions = "org.freedesktop.login1.Manager.ListSessions"
	sessionIface = "org.freedesktop.login1.Session"

	Days        = 30
	maxSources  = 100
	maxSessions = 64
	maxFile     = 1 << 20 // /etc/passwd or /etc/group
	maxUsers    = 500
	maxText     = 255
)

// Source is one address's logins over the period.
type Source struct {
	Source   string `json:"source"`
	Accepted int    `json:"accepted"`
	Failed   int    `json:"failed"`
	Last     int64  `json:"last"` // unix ms
}

// Day is one local day's logins.
type Day struct {
	Day      string `json:"day"` // YYYY-MM-DD
	Accepted int    `json:"accepted"`
	Failed   int    `json:"failed"`
}

// Session is one open logind session.
type Session struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	UID        uint32 `json:"uid"`
	Seat       string `json:"seat,omitempty"`
	TTY        string `json:"tty,omitempty"`
	Type       string `json:"type,omitempty"`
	Service    string `json:"service,omitempty"`
	State      string `json:"state,omitempty"`
	Remote     bool   `json:"remote"`
	RemoteHost string `json:"remoteHost,omitempty"`
	Since      int64  `json:"since,omitempty"` // unix ms
}

// User is one account. UID is -1 for a member of a group that /etc/passwd
// does not list, which still counts: it may come from a directory service.
type User struct {
	Name   string   `json:"name"`
	UID    int      `json:"uid"`
	Login  bool     `json:"login"`
	Groups []string `json:"groups"`
	Sudo   bool     `json:"sudo,omitempty"`
	Docker bool     `json:"docker,omitempty"`
}

// Report is GET /api/v1/security/access.
type Report struct {
	Days          int       `json:"days"`
	Limited       bool      `json:"limited,omitempty"` // the system journal is out of reach
	Truncated     bool      `json:"truncated,omitempty"`
	Sources       []Source  `json:"sources"`
	More          int       `json:"more,omitempty"` // sources past the list's bound
	ByDay         []Day     `json:"byDay"`          // newest first
	Sessions      []Session `json:"sessions"`
	SessionsError bool      `json:"sessionsError,omitempty"`
	Users         []User    `json:"users"`
	UsersError    bool      `json:"usersError,omitempty"`
}

// Bus is the subset of dbusx.Conn used here.
type Bus interface {
	GetAll(ctx context.Context, dest string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Close() error
}

// Config configures an API.
type Config struct {
	Run           func(ctx context.Context, name string, args ...string) (safeexec.Result, error)
	Bus           func(ctx context.Context) (Bus, error)
	CanReadSystem func() bool
	EtcRoot       string // default /etc
	Now           func() time.Time
}

// API serves the report.
type API struct{ cfg Config }

// New returns an API.
func New(cfg Config) *API {
	if cfg.EtcRoot == "" {
		cfg.EtcRoot = "/etc"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &API{cfg: cfg}
}

func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if len(s) > maxText {
		s = strings.ToValidUTF8(s[:maxText], "")
	}
	return s
}

// The user name in these lines is whatever a stranger typed, so each pattern
// is greedy up to the last " from": a name cannot pose as the source. Failed
// lines for an unknown user are left out, since "Invalid user" already counted
// that connection, and key-only servers log nothing else for it.
var (
	acceptedLine = regexp.MustCompile(`^Accepted \S+ for .* from (\S+) port \d+ ssh2(?:: \S+ \S+)?$`)
	failedLine   = regexp.MustCompile(`^Failed (\S+) for (invalid user )?.* from (\S+) port \d+ ssh2$`)
	invalidLine  = regexp.MustCompile(`^Invalid user .* from (\S+) port \d+$`)
	hostName     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// source is a line's address in one spelling, or "" if it is not an address
// or a host name (sshd's UseDNS gives names, which DNS's owner chooses). The
// zone goes: ParseAddr allows any text in it, and it says nothing about who
// connected.
func source(s string) string {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone("").String()
	}
	if s != "UNKNOWN" && hostName.MatchString(s) {
		return s
	}
	return ""
}

// classify reads one sshd message. User names are dropped here: people type
// passwords into the login prompt, and that should reach no page.
func classify(msg string) (src string, accepted, ok bool) {
	if m := acceptedLine.FindStringSubmatch(msg); m != nil {
		src, accepted = m[1], true
	} else if m := failedLine.FindStringSubmatch(msg); m != nil && m[1] != "none" && m[2] == "" {
		src = m[3]
	} else if m := invalidLine.FindStringSubmatch(msg); m != nil {
		src = m[1]
	}
	src = source(src)
	return src, accepted, src != ""
}

// parseSSH counts journal entries by source and by local day.
func parseSSH(b []byte, loc *time.Location) (sources []Source, more int, days []Day, lines int) {
	bySource, byDay := map[string]*Source{}, map[string]*Day{}
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		lines++
		var e struct {
			Message string `json:"MESSAGE"`
			Stamp   string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		us, err := strconv.ParseInt(e.Stamp, 10, 64)
		if err != nil || us <= 0 {
			continue
		}
		src, accepted, ok := classify(e.Message)
		if !ok {
			continue
		}
		s := bySource[src]
		if s == nil {
			s = &Source{Source: src}
			bySource[src] = s
		}
		name := time.UnixMicro(us).In(loc).Format(time.DateOnly)
		d := byDay[name]
		if d == nil {
			d = &Day{Day: name}
			byDay[name] = d
		}
		if accepted {
			s.Accepted++
			d.Accepted++
		} else {
			s.Failed++
			d.Failed++
		}
		s.Last = max(s.Last, us/1000)
	}
	sources, days = []Source{}, []Day{}
	for _, s := range bySource {
		sources = append(sources, *s)
	}
	for _, d := range byDay {
		days = append(days, *d)
	}
	// A source that got in matters more than one that keeps failing.
	slices.SortFunc(sources, func(a, b Source) int {
		return cmp.Or(cmp.Compare(min(b.Accepted, 1), min(a.Accepted, 1)), cmp.Compare(b.Failed, a.Failed), cmp.Compare(a.Source, b.Source))
	})
	slices.SortFunc(days, func(a, b Day) int { return cmp.Compare(b.Day, a.Day) })
	if len(sources) > maxSources {
		more = len(sources) - maxSources
		sources = sources[:maxSources]
	}
	return sources, more, days, lines
}

func prop[T any](p map[string]dbus.Variant, name string) T {
	v, _ := p[name].Value().(T)
	return v
}

// parseSession makes a Session from its row of ListSessions and its
// properties, or false for one that is not a person's: systemd lists each
// user's service manager as a session too.
func parseSession(id string, uid uint32, user, seat string, p map[string]dbus.Variant) (Session, bool) {
	if !strings.HasPrefix(prop[string](p, "Class"), "user") {
		return Session{}, false
	}
	return Session{ID: clean(id), User: clean(user), UID: uid, Seat: clean(seat),
		TTY: clean(prop[string](p, "TTY")), Type: clean(prop[string](p, "Type")), Service: clean(prop[string](p, "Service")),
		State: clean(prop[string](p, "State")), Remote: prop[bool](p, "Remote"), RemoteHost: clean(prop[string](p, "RemoteHost")),
		Since: int64(prop[uint64](p, "Timestamp") / 1000)}, true // #nosec G115 -- microseconds since 1970
}

// sessions asks logind. Any session it will not describe is skipped: it has
// just closed.
func (a *API) sessions(ctx context.Context) ([]Session, error) {
	bus, err := a.cfg.Bus(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bus.Close() }()
	var rows [][]any
	if err := bus.Call(ctx, logindDest, logindPath, listSessions, []any{&rows}); err != nil {
		return nil, err
	}
	out := []Session{}
	for _, r := range rows {
		if len(r) != 5 || len(out) == maxSessions {
			continue
		}
		id, ok1 := r[0].(string)
		uid, ok2 := r[1].(uint32)
		user, ok3 := r[2].(string)
		seat, ok4 := r[3].(string)
		p, ok5 := r[4].(dbus.ObjectPath)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			continue
		}
		props, err := bus.GetAll(ctx, logindDest, p, sessionIface)
		if err != nil {
			continue
		}
		if s, ok := parseSession(id, uid, user, seat, props); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func readBounded(name string) ([]byte, error) {
	f, err := os.Open(name) // #nosec G304 -- a fixed file under a fixed root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxFile))
}

// parseUsers joins passwd and group. It reads no password field: they hold
// "x", and the real ones in /etc/shadow are never opened.
func parseUsers(passwd, group []byte) []User {
	type grp struct {
		name    string
		members []string
	}
	byGID := map[string]string{}
	groups := []grp{}
	for line := range strings.Lines(string(group)) {
		f := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(f) != 4 || f[0] == "" {
			continue
		}
		byGID[f[2]] = f[0]
		groups = append(groups, grp{f[0], strings.Split(f[3], ",")})
	}
	users := map[string]*User{}
	for line := range strings.Lines(string(passwd)) {
		f := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(f) != 7 || f[0] == "" {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		shell := path.Base(f[6])
		u := &User{Name: f[0], UID: uid, Login: shell != "nologin" && shell != "false" && f[6] != "", Groups: []string{}}
		if g, ok := byGID[f[3]]; ok {
			u.Groups = append(u.Groups, g)
		}
		users[f[0]] = u
	}
	for _, g := range groups {
		for _, m := range g.members {
			if m == "" {
				continue
			}
			u := users[m]
			if u == nil {
				if g.name != "sudo" && g.name != "docker" {
					continue
				}
				u = &User{Name: m, UID: -1, Groups: []string{}}
				users[m] = u
			}
			if !slices.Contains(u.Groups, g.name) {
				u.Groups = append(u.Groups, g.name)
			}
		}
	}
	out := []User{}
	for _, u := range users {
		u.Sudo, u.Docker = slices.Contains(u.Groups, "sudo"), slices.Contains(u.Groups, "docker")
		person := u.UID == 0 || (u.UID >= 1000 && u.UID < 65534)
		if !person && !u.Sudo && !u.Docker {
			continue
		}
		slices.Sort(u.Groups)
		u.Name = clean(u.Name)
		for i, g := range u.Groups {
			u.Groups[i] = clean(g)
		}
		out = append(out, *u)
	}
	// Those with root's power first, so a bound cannot hide one.
	power := func(u User) int {
		if u.UID == 0 || u.Sudo || u.Docker {
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b User) int {
		known := func(u User) int {
			if u.UID < 0 {
				return 1
			}
			return 0
		}
		return cmp.Or(cmp.Compare(power(a), power(b)), cmp.Compare(known(a), known(b)), cmp.Compare(a.UID, b.UID), cmp.Compare(a.Name, b.Name))
	})
	if len(out) > maxUsers {
		out = out[:maxUsers]
	}
	return out
}

// Read builds the report. Each part fails on its own.
func (a *API) Read(ctx context.Context) Report {
	r := Report{Days: Days, Sources: []Source{}, ByDay: []Day{}, Sessions: []Session{}, Users: []User{}}
	now := a.cfg.Now()
	since := now.Add(-Days * 24 * time.Hour).Unix()
	if res, err := a.cfg.Run(ctx, "journalctl-ssh", safeexec.SSHArgs(since)...); err == nil || res.ExitCode == 1 {
		var lines int
		r.Sources, r.More, r.ByDay, lines = parseSSH(res.Stdout, now.Location())
		r.Truncated = lines >= safeexec.MaxSSHLines
	} else if !errors.Is(err, context.Canceled) {
		r.Limited = true
	}
	if a.cfg.CanReadSystem != nil && !a.cfg.CanReadSystem() {
		r.Limited = true
	}
	if s, err := a.sessions(ctx); err == nil {
		r.Sessions = s
	} else {
		r.SessionsError = true
	}
	passwd, err1 := readBounded(a.cfg.EtcRoot + "/passwd")
	group, err2 := readBounded(a.cfg.EtcRoot + "/group")
	if err1 != nil || err2 != nil {
		r.UsersError = true
	} else {
		r.Users = parseUsers(passwd, group)
	}
	return r
}

// Handle serves GET /api/v1/security/access.
func (a *API) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(a.Read(r.Context()))
}

// Failures counts the failed SSH logins in journalctl-ssh's output and
// lists their sources, most failures first, at most 10 (automations'
// ssh_failed trigger, ROADMAP 12.4). Sources are addresses or plain host
// names only, as the Access page shows them (S16j).
func Failures(b []byte) (failed int, sources []string) {
	list, _, _, _ := parseSSH(b, time.Local)
	sort.Slice(list, func(i, j int) bool { return list[i].Failed > list[j].Failed })
	for _, s := range list {
		failed += s.Failed
		if s.Failed > 0 && len(sources) < 10 {
			sources = append(sources, s.Source)
		}
	}
	return failed, sources
}
