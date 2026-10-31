package alerts

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"regexp/syntax"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	// LogMetric is the metric of a log-pattern rule, whose target is the
	// pattern (RE2).
	LogMetric = "log.match"
	// maxPattern and maxProgram bound a pattern: Go's regexp is RE2 and
	// linear, so its cost is the compiled size times the line's length.
	maxPattern = 200
	maxProgram = 4000
	// MaxLogRules is how many enabled log rules are watched.
	MaxLogRules = 16
	// PulseHold is how long one event keeps its incident open. It is the
	// log alerts' hysteresis and rate limit: a flood is one incident and
	// one notification, and it resolves after this long without a match.
	PulseHold = 10 * time.Minute
	logEvery  = 30 * time.Second
)

// Pulse turns events, which have no end, into a state an incident can
// follow. It is not safe for concurrent use.
type Pulse struct {
	until map[string]time.Time
}

// Hit records an event of key.
func (p *Pulse) Hit(key string, now time.Time) {
	if p.until == nil {
		p.until = map[string]time.Time{}
	}
	p.until[key] = now.Add(PulseHold)
}

// Values is 1 for every key hit in the last PulseHold.
func (p *Pulse) Values(now time.Time) map[string]float64 {
	out := make(map[string]float64, len(p.until))
	for k, t := range p.until {
		if now.After(t) {
			delete(p.until, k)
			continue
		}
		out[k] = 1
	}
	return out
}

// CompilePattern checks a log pattern's size and that it cannot match
// everything, which would be an alert on every line.
func CompilePattern(s string) (*regexp.Regexp, error) {
	if s == "" {
		return nil, errors.New("a log rule needs a pattern")
	}
	if len(s) > maxPattern {
		return nil, errors.New("a log pattern is at most 200 characters")
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return nil, errors.New("not a valid pattern (RE2 syntax)")
	}
	parsed, err := syntax.Parse(s, syntax.Perl)
	if err != nil {
		return nil, errors.New("not a valid pattern (RE2 syntax)")
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil || len(prog.Inst) > maxProgram {
		return nil, errors.New("this log pattern is too large")
	}
	if re.MatchString("") {
		return nil, errors.New("this log pattern matches every line")
	}
	return re, nil
}

// JournalReader returns the messages after cursor and the new cursor;
// with no cursor, no messages and the journal's end.
type JournalReader func(ctx context.Context, cursor string) (messages []string, next string, err error)

// LogWatcher matches new journal lines against the log rules. It reads
// the journal only while such a rule is enabled, and the line that matched
// goes no further than this loop: no incident, notification or log holds
// it, since a line can hold anything.
type LogWatcher struct {
	Read    JournalReader
	Rules   func(ctx context.Context) ([]store.AlertRule, error)
	Observe func(ctx context.Context, now time.Time, values map[string]float64)
	Log     *slog.Logger
	Every   time.Duration // default 30 s

	cursor string
	pulse  Pulse
}

// patterns compiles the enabled log rules' patterns.
func (w *LogWatcher) patterns(ctx context.Context) []*regexp.Regexp {
	rules, err := w.Rules(ctx)
	if err != nil {
		return nil
	}
	var out []*regexp.Regexp
	seen := map[string]bool{}
	for _, r := range rules {
		if !r.Enabled || r.Metric != LogMetric || seen[r.Target] || len(out) == MaxLogRules {
			continue
		}
		seen[r.Target] = true
		if re, err := CompilePattern(r.Target); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// Step is one round.
func (w *LogWatcher) Step(ctx context.Context, now time.Time) {
	res := w.patterns(ctx)
	if len(res) == 0 {
		// Starts from the end again, so a rule added later does not
		// alert on what happened before it.
		w.cursor = ""
		w.Observe(ctx, now, map[string]float64{})
		return
	}
	lines, next, err := w.Read(ctx, w.cursor)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("alerts: read the journal", "err", err)
		}
		return
	}
	if next != "" {
		w.cursor = next
	}
	for _, re := range res {
		for _, l := range lines {
			if re.MatchString(l) {
				w.pulse.Hit(re.String(), now)
				break
			}
		}
	}
	w.Observe(ctx, now, w.pulse.Values(now))
}

// Run polls until ctx ends.
func (w *LogWatcher) Run(ctx context.Context) {
	every := w.Every
	if every == 0 {
		every = logEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.Step(ctx, now)
		}
	}
}
