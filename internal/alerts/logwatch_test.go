package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestCompilePattern(t *testing.T) {
	for _, ok := range []string{"error", `(?i)segfault at [0-9a-f]+`, `^kernel: .*oom`} {
		if _, err := CompilePattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".*", "a*", "|x", "(", `(a)\1`, "(?=x)", strings.Repeat("a", 201),
		"(a{1000}){1000}", ".{1000}.{1000}.{1000}.{1000}.{1000}.{1000}.{1000}x"} {
		if _, err := CompilePattern(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLogRuleValidation(t *testing.T) {
	r := store.AlertRule{Name: "x", Metric: LogMetric, Target: "panic", Op: ">", Severity: "warning"}
	if err := Validate(r); err != nil {
		t.Fatal(err)
	}
	r.Target = ""
	if Validate(r) == nil {
		t.Fatal("a log rule without a pattern")
	}
	r.Target = "("
	if Validate(r) == nil {
		t.Fatal("a bad pattern")
	}
}

type journal struct {
	lines []string
	calls []string
	err   error
}

func (j *journal) read(_ context.Context, cursor string) ([]string, string, error) {
	j.calls = append(j.calls, cursor)
	if j.err != nil {
		return nil, "", j.err
	}
	if cursor == "" {
		return nil, "c1", nil
	}
	l := j.lines
	j.lines = nil
	return l, "c2", nil
}

func TestLogWatcher(t *testing.T) {
	j := &journal{}
	rules := []store.AlertRule{{Metric: LogMetric, Target: "panic", Enabled: true}, {Metric: LogMetric, Target: "off", Enabled: false}, {Metric: "cpu.usage"}}
	var got []map[string]float64
	w := &LogWatcher{Read: j.read,
		Rules:   func(context.Context) ([]store.AlertRule, error) { return rules, nil },
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) { got = append(got, v) }}
	ctx, now := context.Background(), time.Now()
	w.Step(ctx, now) // finds the end only
	j.lines = []string{"fine", "kernel panic here", "off"}
	w.Step(ctx, now.Add(time.Second))
	if l := got[len(got)-1]; len(l) != 1 || l["panic"] != 1 {
		t.Fatalf("%v", got)
	}
	// Quiet, but within the hold: still open, so one notification.
	w.Step(ctx, now.Add(PulseHold))
	if l := got[len(got)-1]; l["panic"] != 1 {
		t.Fatalf("%v", got)
	}
	w.Step(ctx, now.Add(PulseHold+2*time.Second))
	if l := got[len(got)-1]; len(l) != 0 {
		t.Fatalf("%v", got)
	}
	if j.calls[0] != "" || j.calls[1] != "c1" {
		t.Fatalf("%v", j.calls)
	}
}

// No rule, no journal reading: nothing to pay for when idle.
func TestLogWatcherIdle(t *testing.T) {
	j := &journal{}
	w := &LogWatcher{Read: j.read,
		Rules:   func(context.Context) ([]store.AlertRule, error) { return nil, nil },
		Observe: func(context.Context, time.Time, map[string]float64) {}}
	w.Step(context.Background(), time.Now())
	if len(j.calls) != 0 {
		t.Fatalf("%v", j.calls)
	}
}

// A read failure says nothing: an open incident stays open.
func TestLogWatcherReadError(t *testing.T) {
	j := &journal{err: errors.New("no")}
	n := 0
	w := &LogWatcher{Read: j.read,
		Rules: func(context.Context) ([]store.AlertRule, error) {
			return []store.AlertRule{{Metric: LogMetric, Target: "x", Enabled: true}}, nil
		},
		Observe: func(context.Context, time.Time, map[string]float64) { n++ }}
	w.Step(context.Background(), time.Now())
	if n != 0 {
		t.Fatal(n)
	}
}

func TestDescribeHidesLogs(t *testing.T) {
	_, body := Describe(store.AlertEvent{Metric: LogMetric, RuleName: "r", Instance: "password=.*"})
	if strings.Contains(body, "password") {
		t.Fatal(body)
	}
}

func FuzzCompilePattern(f *testing.F) {
	for _, s := range []string{"error", "(a|b)*c", `\p{Greek}+`, "a{1000}"} {
		f.Add(s, "a log line")
	}
	f.Fuzz(func(t *testing.T, p, line string) {
		re, err := CompilePattern(p)
		if err != nil {
			return
		}
		if len(p) > maxPattern {
			t.Fatal("too long")
		}
		if len(line) > maxTailLineForFuzz {
			line = line[:maxTailLineForFuzz]
		}
		start := time.Now()
		re.MatchString(line)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%q took %v", p, d)
		}
	})
}

const maxTailLineForFuzz = 4 << 10
