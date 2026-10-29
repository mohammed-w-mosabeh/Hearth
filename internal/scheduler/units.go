// Package scheduler manages Hearth's scheduled jobs (SPEC 14): real
// systemd .service + .timer pairs named hearth-job-<id>. A user job's
// units live in ~/.config/systemd/user and its script in Hearth's data
// directory; a root job's units live in /etc/systemd/system and its script
// in /var/lib/hearth/jobs/<id>/run, written by hearth-broker (SECURITY
// S13). The unit files are the record of a job: its name and whether to
// notify on failure are X-Hearth-* keys systemd ignores.
package scheduler

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Prefix starts every job unit name.
const Prefix = "hearth-job-"

// Schedule kinds.
const (
	KindCalendar = "calendar" // OnCalendar=
	KindBoot     = "boot"     // OnBootSec=: once, that long after boot
	KindInterval = "interval" // OnUnitActiveSec=: again that long after each run
)

// Schedule is when a job runs.
type Schedule struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Job is what the form edits.
type Job struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Script   string   `json:"script"`
	Schedule Schedule `json:"schedule"`
	Timeout  int      `json:"timeout"` // seconds
	Notify   bool     `json:"notify"`  // alert when a run fails
}

// Limits.
const (
	MaxName    = 80
	MaxScript  = 64 << 10
	MaxTimeout = 7 * 24 * 3600
)

var (
	idRe = regexp.MustCompile(`^[a-f0-9]{8}$`)
)

// ValidID reports whether s is a job id.
func ValidID(s string) bool { return idRe.MatchString(s) }

// UnitBase is a job's unit name without suffix.
func UnitBase(id string) string { return Prefix + id }

// CalendarSyntax checks the characters of an OnCalendar expression;
// systemd-analyze calendar then decides whether it means anything.
func CalendarSyntax(s string) error { return safeexec.CalendarExpr(s) }

// SpanSyntax checks a time span ("5min", "1h 30min").
func SpanSyntax(s string) error { return safeexec.TimeSpan(s) }

// Validate checks everything but the schedule's meaning.
func (j Job) Validate() error {
	if j.ID != "" && !ValidID(j.ID) {
		return errors.New("bad job id")
	}
	if err := ValidName(j.Name); err != nil {
		return err
	}
	if err := ValidScript(j.Script); err != nil {
		return err
	}
	if j.Timeout < 1 || j.Timeout > MaxTimeout {
		return fmt.Errorf("the timeout must be 1 to %d seconds", MaxTimeout)
	}
	switch j.Schedule.Kind {
	case KindCalendar:
		return CalendarSyntax(j.Schedule.Value)
	case KindBoot, KindInterval:
		return SpanSyntax(j.Schedule.Value)
	}
	return errors.New("the schedule kind must be calendar, boot or interval")
}

// ValidName accepts a one-line name of printable characters.
func ValidName(s string) error {
	if s == "" || utf8.RuneCountInString(s) > MaxName || !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.HasSuffix(s, `\`) {
		return fmt.Errorf("the name must be 1 to %d characters, without leading or trailing spaces or a trailing backslash", MaxName)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return errors.New("the name must be one line of printable characters")
		}
	}
	return nil
}

// ValidScript accepts UTF-8 text without NUL, starting with a #! line.
func ValidScript(s string) error {
	switch {
	case len(s) > MaxScript:
		return fmt.Errorf("the script must be at most %d KiB", MaxScript>>10)
	case !utf8.ValidString(s) || strings.ContainsRune(s, 0):
		return errors.New("the script must be text")
	case !strings.HasPrefix(s, "#!/"):
		return errors.New(`the script must start with an interpreter line such as "#!/bin/sh"`)
	}
	return nil
}

// value escapes a free-text unit file value: '%' starts a specifier.
// (Names have no control characters and no trailing '\', which would
// continue the line.)
func value(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// execPath quotes a program path for ExecStart=: in double quotes, with
// '\' and '"' escaped and '%' as "%%". (systemd expands $VARIABLES only in
// the arguments, never in the program path, so '$' stays as it is.)
func execPath(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	return `"` + r.Replace(p) + `"`
}

// ServiceFile is a job's .service unit; run is the script's path.
func ServiceFile(j Job, run string) string {
	var b strings.Builder
	b.WriteString("# Written by Hearth. Edit the job in Hearth's Scheduler.\n")
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", value(j.Name))
	fmt.Fprintf(&b, "X-Hearth-Job=%s\n", j.ID)
	fmt.Fprintf(&b, "X-Hearth-Notify=%s\n", yesNo(j.Notify))
	b.WriteString("\n[Service]\nType=oneshot\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", execPath(run))
	// Output found by its stream's identifier (safeexec.JobJournalMatches).
	fmt.Fprintf(&b, "SyslogIdentifier=%s\n", UnitBase(j.ID))
	fmt.Fprintf(&b, "TimeoutStartSec=%ds\n", j.Timeout)
	return b.String()
}

// TimerFile is a job's .timer unit.
func TimerFile(j Job) string {
	var b strings.Builder
	b.WriteString("# Written by Hearth. Edit the job in Hearth's Scheduler.\n")
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s (schedule)\n", value(j.Name))
	b.WriteString("\n[Timer]\n")
	switch j.Schedule.Kind {
	case KindCalendar:
		fmt.Fprintf(&b, "OnCalendar=%s\nPersistent=true\n", j.Schedule.Value)
	case KindBoot:
		fmt.Fprintf(&b, "OnBootSec=%s\n", j.Schedule.Value)
	case KindInterval:
		// The first run counts from when the timer starts.
		fmt.Fprintf(&b, "OnActiveSec=%s\nOnUnitActiveSec=%s\n", j.Schedule.Value, j.Schedule.Value)
	}
	fmt.Fprintf(&b, "Unit=%s.service\n", UnitBase(j.ID))
	b.WriteString("\n[Install]\nWantedBy=timers.target\n")
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Parse reads a unit file Hearth wrote: section -> key -> value (the last
// one wins). Comments and blank lines are skipped.
func Parse(s string) map[string]map[string]string {
	out := map[string]map[string]string{}
	sec := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && line[len(line)-1] == ']':
			sec = line[1 : len(line)-1]
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if out[sec] == nil {
				out[sec] = map[string]string{}
			}
			out[sec][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// unescape undoes value.
func unescape(s string) string { return strings.ReplaceAll(s, "%%", "%") }

// FromUnits reads a job back from its unit files (the script is read
// separately). ok is false when they are not a Hearth job's.
func FromUnits(id, service, timer string) (Job, bool) {
	sv, tm := Parse(service), Parse(timer)
	if sv["Unit"]["X-Hearth-Job"] != id {
		return Job{}, false
	}
	j := Job{ID: id, Name: unescape(sv["Unit"]["Description"]), Notify: sv["Unit"]["X-Hearth-Notify"] == "yes"}
	if t, err := strconv.Atoi(strings.TrimSuffix(sv["Service"]["TimeoutStartSec"], "s")); err == nil {
		j.Timeout = t
	}
	t := tm["Timer"]
	switch {
	case t["OnCalendar"] != "":
		j.Schedule = Schedule{Kind: KindCalendar, Value: t["OnCalendar"]}
	case t["OnUnitActiveSec"] != "":
		j.Schedule = Schedule{Kind: KindInterval, Value: t["OnUnitActiveSec"]}
	case t["OnBootSec"] != "":
		j.Schedule = Schedule{Kind: KindBoot, Value: t["OnBootSec"]}
	}
	return j, true
}
