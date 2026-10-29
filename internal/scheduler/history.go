package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Check is what systemd makes of a schedule.
type Check struct {
	Valid      bool    `json:"valid"`
	Error      string  `json:"error,omitempty"`
	Normalized string  `json:"normalized,omitempty"`
	Next       []int64 `json:"next,omitempty"` // Unix ms of the next runs (calendar)
}

// CheckSchedule asks systemd-analyze whether a schedule means anything,
// and when it would run next.
func CheckSchedule(ctx context.Context, run Runner, s Schedule) Check {
	var name string
	var args []string
	switch s.Kind {
	case KindCalendar:
		if err := CalendarSyntax(s.Value); err != nil {
			return Check{Error: "not a calendar expression"}
		}
		name, args = "calendar-check", []string{"calendar", "--iterations=5", "--", s.Value}
	case KindBoot, KindInterval:
		if err := SpanSyntax(s.Value); err != nil {
			return Check{Error: "not a time span (for example 15min, 2h, 1d)"}
		}
		name, args = "timespan-check", []string{"timespan", "--", s.Value}
	default:
		return Check{Error: "unknown schedule kind"}
	}
	res, err := run(ctx, name, args...)
	if err != nil {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = "systemd does not understand this schedule"
		}
		return Check{Error: clip(msg, 200)}
	}
	return parseAnalyze(string(res.Stdout))
}

// parseAnalyze reads systemd-analyze calendar or timespan output.
func parseAnalyze(out string) Check {
	c := Check{Valid: true}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Normalized form", "Human":
			c.Normalized = v
		case "(in UTC)":
			// "Thu 2026-10-29 07:30:00 UTC"
			f := strings.Fields(v)
			if len(f) == 4 {
				if t, err := time.Parse("2006-01-02 15:04:05", f[1]+" "+f[2]); err == nil {
					c.Next = append(c.Next, t.UnixMilli())
				}
			}
		}
	}
	return c
}

// Run is one run of a job, from the journal.
type Run struct {
	Start  int64    `json:"start"` // Unix ms
	End    int64    `json:"end,omitempty"`
	Result string   `json:"result"` // success, failed, running
	Exit   *int     `json:"exit,omitempty"`
	Output []string `json:"output"`
	// More is set when output lines were left out.
	More bool `json:"more,omitempty"`
}

// History limits.
const (
	historyLines = 1000
	maxOutput    = 50
	maxLine      = 500
)

// ErrNoJournal is returned when the journal cannot be read.
var ErrNoJournal = errors.New("the journal could not be read")

// History reads a job's recent runs from the journal, newest first.
func History(ctx context.Context, run Runner, scope, id string) ([]Run, error) {
	args := append(append([]string{}, safeexec.JobJournalFixedArgs...), safeexec.JobJournalMatches(UnitBase(id), scope == ScopeUser)...)
	res, err := run(ctx, "journalctl-job", args...)
	if err != nil {
		return nil, ErrNoJournal
	}
	return parseRuns(res.Stdout, res.Truncated), nil
}

// parseRuns groups journal entries by invocation.
func parseRuns(b []byte, truncated bool) []Run {
	if truncated {
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			b = b[:i]
		}
	}
	runs := map[string]*Run{}
	var order []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		get := func(k string) string {
			var s string
			if json.Unmarshal(e[k], &s) != nil {
				return ""
			}
			return s
		}
		ts, _ := strconv.ParseInt(get("__REALTIME_TIMESTAMP"), 10, 64)
		ms := ts / 1000
		inv, output := get("_SYSTEMD_INVOCATION_ID"), true
		if inv == "" {
			inv, output = get("INVOCATION_ID"), false
			if inv == "" {
				inv = get("USER_INVOCATION_ID")
			}
		}
		if inv == "" {
			// Output of a process that exited before journald looked up
			// its cgroup: it belongs to the run that started last.
			if get("_TRANSPORT") == "stdout" {
				var last *Run
				for _, o := range order {
					if r := runs[o]; r.Start <= ms && (last == nil || r.Start >= last.Start) {
						last = r
					}
				}
				if last != nil {
					addOutput(last, e["MESSAGE"])
				}
			}
			continue
		}
		r := runs[inv]
		if r == nil {
			r = &Run{Start: ms, Result: "running", Output: []string{}}
			runs[inv] = r
			order = append(order, inv)
		}
		if ms > r.End {
			r.End = ms
		}
		if output {
			addOutput(r, e["MESSAGE"])
			continue
		}
		if s := get("EXIT_STATUS"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				r.Exit = &n
			}
		}
		switch get("JOB_RESULT") {
		case "done":
			r.Result = "success"
		case "":
		default:
			r.Result = "failed"
		}
	}
	out := make([]Run, 0, len(order))
	for _, inv := range order {
		r := runs[inv]
		if r.Result == "success" && r.Exit == nil {
			zero := 0
			r.Exit = &zero
		}
		if r.Result == "running" {
			r.End = 0
		}
		out = append(out, *r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out
}

// addOutput adds an output line to a run, up to maxOutput.
func addOutput(r *Run, msg json.RawMessage) {
	if len(r.Output) < maxOutput {
		r.Output = append(r.Output, clip(message(msg), maxLine))
	} else {
		r.More = true
	}
}

// message is a journal MESSAGE, which is a string or, for binary data, an
// array of bytes.
func message(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		for _, n := range ints {
			bs = append(bs, byte(n)) // #nosec G115 -- journal bytes
		}
		return strings.ToValidUTF8(string(bs), "�")
	}
	return ""
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
