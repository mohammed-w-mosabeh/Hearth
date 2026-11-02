package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// ActionLynis is the polkit action for running Lynis and scheduling it.
	ActionLynis = "org.hearth.lynis.run"
	// LynisUnit runs the audit. It is a unit of its own for the reason
	// hearth-needrestart.service is: an audit from inside the broker's
	// sandbox would see little, and would report what it could not see.
	LynisUnit = "hearth-lynis.service"
	// LynisTimer exists only while a schedule is set.
	LynisTimer = "hearth-lynis.timer"
	// LynisOutput is the last finished report. The unit renames it into
	// place only after a run ended well.
	LynisOutput = "/var/lib/hearth/lynis-report.dat"
	// LynisBinary tells whether the optional tool is there.
	LynisBinary = "/usr/sbin/lynis"
	// LynisUnitDir is where the timer is written.
	LynisUnitDir = "/etc/systemd/system"

	// LynisTimeout bounds lynis.run: the unit's own 15 min limit, and a
	// polkit dialog.
	LynisTimeout = 17 * time.Minute

	maxLynisOutput   = 4 << 20
	maxLynisFindings = 500
	maxLynisText     = 400
)

// lynisCalendars is every schedule there is; a timer is rendered from this
// table and nothing else.
var lynisCalendars = map[string]string{"weekly": "weekly", "monthly": "monthly"}

// lynisTestRe is the shape of a Lynis test ID (AUTH-9286, CUST-0010), or
// LYNIS, the name it files notes about itself under.
var lynisTestRe = regexp.MustCompile(`^[A-Z]{2,8}(-[0-9]{1,6})?$`)

// LynisFinding is one warning or suggestion. Test is empty if the report's
// was not a test ID.
type LynisFinding struct {
	Test     string `json:"test"`
	Text     string `json:"text"`
	Details  string `json:"details,omitempty"`
	Solution string `json:"solution,omitempty"`
}

// LynisReport is what Hearth keeps of a report. The file also names the
// host, its users, its packages and more: none of that leaves the broker.
type LynisReport struct {
	Time        int64          `json:"time"` // unix ms the run finished
	Index       int            `json:"index"`
	Version     string         `json:"version,omitempty"`
	Warnings    []LynisFinding `json:"warnings"`
	Suggestions []LynisFinding `json:"suggestions"`
	Truncated   bool           `json:"truncated,omitempty"`
}

// LynisState is lynis.report's result.
type LynisState struct {
	Installed bool         `json:"installed"`
	Schedule  string       `json:"schedule"` // off, weekly or monthly
	Report    *LynisReport `json:"report,omitempty"`
}

// LynisScheduleParams are lynis.schedule's params.
type LynisScheduleParams struct {
	Every string `json:"every"` // off, weekly or monthly
}

// LynisConfig configures LynisMethods.
type LynisConfig struct {
	Bus     func(ctx context.Context) (SystemBus, error)
	Output  string // default LynisOutput
	Binary  string // default LynisBinary
	UnitDir string // default LynisUnitDir
	Poll    time.Duration
}

// parseLynisFinding reads "TEST|text|details|solution|". Lynis writes "-"
// for a field it has nothing for.
func parseLynisFinding(s string) (LynisFinding, bool) {
	p := strings.SplitN(strings.TrimSuffix(s, "|"), "|", 4)
	for len(p) < 4 {
		p = append(p, "")
	}
	field := func(i int) string {
		v := cleanText(p[i], maxLynisText)
		if v == "-" {
			return ""
		}
		return v
	}
	f := LynisFinding{Text: field(1), Details: field(2), Solution: field(3)}
	if lynisTestRe.MatchString(p[0]) {
		f.Test = p[0]
	}
	return f, f.Text != ""
}

// parseLynis reads a report. A report without its end line or a valid
// index is a run that failed, never a clean machine.
func parseLynis(r io.Reader) (LynisReport, error) {
	out := LynisReport{Index: -1, Warnings: []LynisFinding{}, Suggestions: []LynisFinding{}}
	ended := false
	add := func(list *[]LynisFinding, s string) {
		f, ok := parseLynisFinding(s)
		switch {
		case !ok:
		case len(*list) >= maxLynisFindings:
			out.Truncated = true
		default:
			*list = append(*list, f)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "hardening_index":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 && n <= 100 {
				out.Index = n
			}
		case "lynis_version":
			out.Version = cleanText(val, 32)
		case "report_datetime_end":
			ended = true
		case "warning[]":
			add(&out.Warnings, val)
		case "suggestion[]":
			add(&out.Suggestions, val)
		}
	}
	if err := sc.Err(); err != nil || !ended || out.Index < 0 {
		return out, Errorf(CodeFailed, "lynis: no usable report")
	}
	return out, nil
}

func (c LynisConfig) read() (*LynisReport, error) {
	f, err := os.Open(c.Output) // #nosec G304 -- a fixed path in the 0700 state directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, Errorf(CodeFailed, "lynis: no report")
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, Errorf(CodeFailed, "lynis: no report")
	}
	r, err := parseLynis(io.LimitReader(f, maxLynisOutput))
	if err != nil {
		return nil, err
	}
	r.Time = fi.ModTime().UnixMilli()
	return &r, nil
}

func (c LynisConfig) installed() bool {
	_, err := os.Stat(c.Binary)
	return err == nil
}

// schedule reads the schedule from the timer itself, so it cannot disagree
// with what will run.
func (c LynisConfig) schedule() string {
	b, err := os.ReadFile(filepath.Join(c.UnitDir, LynisTimer)) // #nosec G304 -- a fixed name under a fixed directory
	if err != nil {
		return "off"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "OnCalendar="); ok {
			if _, known := lynisCalendars[v]; known {
				return v
			}
		}
	}
	return "off"
}

func renderLynisTimer(calendar string) string {
	return "# Written by hearth-broker when a Lynis schedule is set (SECURITY S16h).\n" +
		"[Unit]\nDescription=Hearth: Lynis audit on the schedule the user set\n\n" +
		"[Timer]\nOnCalendar=" + calendar + "\nPersistent=true\nRandomizedDelaySec=1h\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

// setSchedule writes or removes the timer. Stopping comes first and the
// file goes last, so a failure leaves a schedule the page can still show.
func (c LynisConfig) setSchedule(ctx context.Context, every string) error {
	bus, err := c.Bus(ctx)
	if err != nil {
		return Errorf(CodeFailed, "the system bus is not available")
	}
	defer func() { _ = bus.Close() }()
	path := filepath.Join(c.UnitDir, LynisTimer)
	var job string
	var carries bool
	var changes [][]any
	failed := func(step string) error { return Errorf(CodeFailed, "lynis: the timer could not be %s", step) }
	if every == "off" {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		// Not loaded, or already stopped: either way it no longer runs.
		_ = bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, LynisTimer, "replace")
		if err := bus.Call(ctx, sdDest, sdPath, sdManager+".DisableUnitFiles", []any{&changes}, []string{LynisTimer}, false); err != nil {
			return failed("disabled")
		}
		if err := os.Remove(path); err != nil {
			return failed("removed")
		}
		if err := bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil); err != nil {
			return failed("reloaded")
		}
		return nil
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	// O_EXCL: a link left in the way is refused, not followed.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a unit file, world-readable like all of them; a fixed name
	if err != nil {
		return failed("written")
	}
	_, werr := f.WriteString(renderLynisTimer(lynisCalendars[every]))
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return failed("written")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return failed("written")
	}
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil); err != nil {
		return failed("reloaded")
	}
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".EnableUnitFiles", []any{&carries, &changes}, []string{LynisTimer}, false, true); err != nil {
		return failed("enabled")
	}
	// Restart, not Start: a timer that was running must take the new time.
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".RestartUnit", []any{&job}, LynisTimer, "replace"); err != nil {
		return failed("started")
	}
	return nil
}

// LynisMethods are the broker's Lynis methods (SECURITY S16h). lynis.report
// only reads the last report, so it needs no polkit; lynis.run takes no
// params and starts one fixed unit, whose unit file holds the whole argv;
// lynis.schedule picks a row of a fixed table.
func LynisMethods(c LynisConfig) []Method {
	if c.Output == "" {
		c.Output = LynisOutput
	}
	if c.Binary == "" {
		c.Binary = LynisBinary
	}
	if c.UnitDir == "" {
		c.UnitDir = LynisUnitDir
	}
	schedule := func(p json.RawMessage) (string, error) {
		var v LynisScheduleParams
		if err := decodeParams(p, &v); err != nil {
			return "", err
		}
		if _, ok := lynisCalendars[v.Every]; !ok && v.Every != "off" {
			return "", badParam("every must be off, weekly or monthly")
		}
		return v.Every, nil
	}
	return []Method{{
		Name: "lynis.report",
		Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			out := LynisState{Installed: c.installed(), Schedule: c.schedule()}
			if !out.Installed {
				return out, nil
			}
			r, err := c.read()
			if err != nil {
				return nil, err
			}
			out.Report = r
			return out, nil
		},
	}, {
		Name: "lynis.run", Privileged: true, Action: ActionLynis, Timeout: LynisTimeout,
		Check: func(_ context.Context, p json.RawMessage) error { return noParams(p) },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			if !c.installed() {
				return nil, Errorf(CodeFailed, "lynis is not installed")
			}
			if err := runOneshot(ctx, c.Bus, c.Poll, "lynis", LynisUnit); err != nil {
				return nil, err
			}
			r, err := c.read()
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, Errorf(CodeFailed, "lynis: no report")
			}
			return LynisState{Installed: true, Schedule: c.schedule(), Report: r}, nil
		},
	}, {
		Name: "lynis.schedule", Privileged: true, Action: ActionLynis, Timeout: time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := schedule(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			every, err := schedule(p)
			if err != nil {
				return nil, err
			}
			if every != "off" && !c.installed() {
				return nil, Errorf(CodeFailed, "lynis is not installed")
			}
			if err := c.setSchedule(ctx, every); err != nil {
				return nil, err
			}
			return map[string]string{"schedule": every}, nil
		},
	}}
}
