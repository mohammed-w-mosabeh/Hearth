// Package crashes reads what went wrong from the journal (SPEC 8, ROADMAP
// 6.3): programs that crashed, as systemd-coredump recorded them, and
// processes the kernel killed for lack of memory, each linked to the unit,
// container or app it ran in. It only reads, as the user.
//
// A crash's MESSAGE and its COREDUMP_CMDLINE and COREDUMP_ENVIRON fields can
// hold secrets, so safeexec's fixed argument list never asks for them.
package crashes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Event kinds.
const (
	KindCrash = "crash"
	KindOOM   = "oom"
)

// Owner kinds.
const (
	OwnerUnit      = "unit"
	OwnerContainer = "container"
	OwnerApp       = "app"
)

const (
	maxText     = 255 // bytes of a name, path or signal
	defaultDays = 30
	corePattern = "/proc/sys/kernel/core_pattern"
)

// Owner is what a process ran in.
type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Event is one crash or out-of-memory kill. Names come from the system:
// plain text, escaped by the UI.
type Event struct {
	Time   int64  `json:"t"` // unix ms
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Exe    string `json:"exe,omitempty"`
	Signal string `json:"signal,omitempty"`
	Owner  *Owner `json:"owner,omitempty"`
}

// Day counts one day's events.
type Day struct {
	Day   string `json:"day"` // local date
	Crash int    `json:"crash"`
	OOM   int    `json:"oom"`
}

// Report is GET /api/v1/crashes.
type Report struct {
	Events []Event `json:"events"` // newest first
	Days   []Day   `json:"days"`   // oldest first, only days with events
	// Limited: this user cannot read the system journal, so kernel
	// out-of-memory kills and other users' crashes are missing.
	Limited bool `json:"limited,omitempty"`
	// Coredump: systemd-coredump receives crashes. Without it none are
	// recorded, which does not mean none happened.
	Coredump bool `json:"coredump"`
	// Truncated: more happened than one reply holds.
	Truncated bool `json:"truncated,omitempty"`
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Config configures an API.
type Config struct {
	Run           Runner
	CanReadSystem func() bool
	// ContainerName resolves a full container ID; "" if unknown.
	ContainerName func(id string) string
	// CorePattern is the file naming the crash handler; default /proc's.
	CorePattern string
	Now         func() time.Time
}

// API serves the report.
type API struct{ cfg Config }

// New returns an API.
func New(cfg Config) *API {
	if cfg.CorePattern == "" {
		cfg.CorePattern = corePattern
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &API{cfg: cfg}
}

// clean bounds a system string and drops control characters.
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

var (
	containerScope = regexp.MustCompile(`^docker-([0-9a-f]{64})\.scope$`)
	containerDir   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	userManager    = regexp.MustCompile(`^user@[0-9]+\.service$`)
)

// classify finds what a cgroup path belongs to, innermost first.
func classify(cgroup string, name func(string) string) *Owner {
	parts := strings.Split(cgroup, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		id := ""
		if m := containerScope.FindStringSubmatch(p); m != nil {
			id = m[1]
		} else if containerDir.MatchString(p) && i > 0 && parts[i-1] == "docker" {
			id = p
		}
		switch {
		case id != "":
			n := ""
			if name != nil {
				n = name(id)
			}
			if n == "" {
				n = id[:12]
			}
			return &Owner{Kind: OwnerContainer, Name: clean(n)}
		case strings.HasSuffix(p, ".service") && !userManager.MatchString(p):
			return &Owner{Kind: OwnerUnit, Name: clean(p)}
		case strings.HasPrefix(p, "app-") && strings.HasSuffix(p, ".scope"):
			return &Owner{Kind: OwnerApp, Name: clean(strings.TrimSuffix(strings.TrimPrefix(p, "app-"), ".scope"))}
		}
	}
	return nil
}

// field reads a journal field. A field that is not valid text, or that
// was logged twice, comes as an array: it is left out.
func field(e map[string]json.RawMessage, k string) string {
	var s string
	if err := json.Unmarshal(e[k], &s); err != nil {
		return ""
	}
	return s
}

// lines calls f on each JSON object of journalctl's output.
func lines(b []byte, f func(e map[string]json.RawMessage, ms int64)) {
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		var e map[string]json.RawMessage
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		us, err := strconv.ParseInt(field(e, "__REALTIME_TIMESTAMP"), 10, 64)
		if err != nil || us <= 0 {
			continue
		}
		f(e, us/1000)
	}
}

// parseCrashes reads systemd-coredump's entries.
func parseCrashes(b []byte, name func(string) string) []Event {
	out := []Event{}
	lines(b, func(e map[string]json.RawMessage, ms int64) {
		ev := Event{Time: ms, Kind: KindCrash, Name: clean(field(e, "COREDUMP_COMM")),
			Exe: clean(field(e, "COREDUMP_EXE")), Signal: clean(field(e, "COREDUMP_SIGNAL_NAME"))}
		if ev.Name == "" {
			ev.Name = ev.Exe
		}
		if ev.Name == "" {
			return
		}
		ev.Owner = classify(field(e, "COREDUMP_CGROUP"), name)
		if ev.Owner == nil || ev.Owner.Kind != OwnerContainer {
			for _, k := range []string{"COREDUMP_USER_UNIT", "COREDUMP_UNIT"} {
				if u := clean(field(e, k)); u != "" && !userManager.MatchString(u) && !strings.HasSuffix(u, ".slice") {
					ev.Owner = &Owner{Kind: OwnerUnit, Name: u}
					break
				}
			}
		}
		out = append(out, ev)
	})
	return out
}

// oomValue reads key=value from the kernel's comma-separated oom-kill line.
func oomValue(msg, key string) string {
	for f := range strings.SplitSeq(msg, ",") {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

// parseOOM reads the kernel's oom-kill lines:
// oom-kill:constraint=...,task_memcg=/system.slice/x.service,task=x,pid=1,uid=0
func parseOOM(b []byte, name func(string) string) []Event {
	out := []Event{}
	lines(b, func(e map[string]json.RawMessage, ms int64) {
		msg := field(e, "MESSAGE")
		task := clean(oomValue(msg, "task"))
		if !strings.HasPrefix(msg, "oom-kill:") || task == "" {
			return
		}
		out = append(out, Event{Time: ms, Kind: KindOOM, Name: task, Owner: classify(oomValue(msg, "task_memcg"), name)})
	})
	return out
}

// byDay counts events per local day, oldest first.
func byDay(events []Event, loc *time.Location) []Day {
	days := map[string]*Day{}
	for _, e := range events {
		k := time.UnixMilli(e.Time).In(loc).Format(time.DateOnly)
		d := days[k]
		if d == nil {
			d = &Day{Day: k}
			days[k] = d
		}
		if e.Kind == KindOOM {
			d.OOM++
		} else {
			d.Crash++
		}
	}
	out := make([]Day, 0, len(days))
	for _, d := range days {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// read runs one query; a search with no match exits 1 with no output.
func (a *API) read(ctx context.Context, name string, args []string) (safeexec.Result, error) {
	res, err := a.cfg.Run(ctx, name, args...)
	if err != nil && res.ExitCode == 1 && len(res.Stdout) == 0 {
		return res, nil
	}
	return res, err
}

// Read builds the report on the last days.
func (a *API) Read(ctx context.Context, days int) (Report, error) {
	since := a.cfg.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	crash, err := a.read(ctx, "journalctl-crashes", safeexec.CrashArgs(since))
	if err != nil {
		return Report{}, err
	}
	oom, err := a.read(ctx, "journalctl-oom", safeexec.OOMArgs(since))
	if err != nil {
		return Report{}, err
	}
	events := append(parseCrashes(crash.Stdout, a.cfg.ContainerName), parseOOM(oom.Stdout, a.cfg.ContainerName)...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time > events[j].Time })
	r := Report{Events: events, Days: byDay(events, time.Local), Truncated: crash.Truncated || oom.Truncated ||
		strings.Count(string(crash.Stdout), "\n") >= safeexec.MaxCrashLines}
	if a.cfg.CanReadSystem != nil {
		r.Limited = !a.cfg.CanReadSystem()
	}
	if b, err := os.ReadFile(a.cfg.CorePattern); err == nil {
		r.Coredump = bytes.Contains(b, []byte("systemd-coredump"))
	}
	return r, nil
}

// parseDays reads the days parameter.
func parseDays(s string) (int, bool) {
	switch s {
	case "":
		return defaultDays, true
	case "7":
		return 7, true
	case "30":
		return 30, true
	case "90":
		return 90, true
	}
	return 0, false
}

// Handle serves GET /api/v1/crashes?days=7|30|90.
func (a *API) Handle(w http.ResponseWriter, r *http.Request) {
	days, ok := parseDays(r.URL.Query().Get("days"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "days must be 7, 30 or 90"})
		return
	}
	report, err := a.Read(r.Context(), days)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "the journal could not be read"})
		return
	}
	_ = json.NewEncoder(w).Encode(report)
}
