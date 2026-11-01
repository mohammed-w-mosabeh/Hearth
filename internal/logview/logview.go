// Package logview is the log explorer (ROADMAP 6.6, SECURITY S21i): the
// newest lines of up to four containers and units, searched with RE2 on
// the server, with repeats folded. Nothing is kept: each request reads,
// trims and answers, so a noisy source costs one reply's memory, and
// sources are read one after another, one request at a time, so that does
// not multiply.
package logview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
)

// Bounds. A reply holds at most MaxSources * MaxShown * MaxLine bytes of text.
const (
	MaxSources = 4
	MaxShown   = 500  // lines per source
	MaxLine    = 2048 // bytes per line
	readLines  = 1000 // lines read per source, before the search
	// containerPrefix marks a container source; a unit is "system/x.service".
	containerPrefix = "container:"
)

// Levels.
const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"
	LevelDebug = "debug"
)

// Line is one line, or a run of the same line. Text comes from the program
// that logged it: plain text, escaped by the UI.
type Line struct {
	Time  int64  `json:"t,omitempty"` // unix ms
	Level string `json:"level"`
	Text  string `json:"text"`
	Count int    `json:"count,omitempty"` // lines folded here, above 1
}

// Source is one column.
type Source struct {
	ID    string `json:"id"`
	Lines []Line `json:"lines"` // oldest first
	// Read is how many lines the search looked at.
	Read      int    `json:"read"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Broker is the subset of broker.Client used here.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// API serves the explorer.
type API struct {
	Broker Broker
	Run    services.Runner
	busy   chan struct{}
}

// New returns an API.
func New(b Broker, run services.Runner) *API {
	return &API{Broker: b, Run: run, busy: make(chan struct{}, 1)}
}

// ValidSource checks a source ID.
func ValidSource(s string) bool {
	if id, ok := strings.CutPrefix(s, containerPrefix); ok {
		return broker.ValidContainer(id)
	}
	scope, unit, ok := strings.Cut(s, "/")
	return ok && (scope == services.ScopeSystem || scope == services.ScopeUser) && safeexec.UnitName(unit) == nil
}

var (
	ansi    = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	digits  = regexp.MustCompile(`[0-9]+`)
	errWord = regexp.MustCompile(`(?i)\b(fatal|panic|critical|crit|error|err|exception|failed)\b`)
	warWord = regexp.MustCompile(`(?i)\b(warn|warning)\b`)
	dbgWord = regexp.MustCompile(`(?i)\b(debug|trace)\b`)
)

// clean strips colour codes and control characters and bounds the length.
func clean(s string) string {
	if len(s) > 2*MaxLine {
		s = s[:2*MaxLine]
	}
	s = strings.Map(func(r rune) rune {
		if r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.ReplaceAllString(strings.ToValidUTF8(s, "�"), ""))
	if len(s) > MaxLine {
		s = strings.ToValidUTF8(s[:MaxLine], "")
	}
	return s
}

// guessLevel reads a level from a line's start, for logs with no priority.
func guessLevel(s string) string {
	if len(s) > 256 {
		s = strings.ToValidUTF8(s[:256], "")
	}
	switch {
	case errWord.MatchString(s):
		return LevelError
	case warWord.MatchString(s):
		return LevelWarn
	case dbgWord.MatchString(s):
		return LevelDebug
	}
	return LevelInfo
}

// prioLevel maps a syslog priority.
func prioLevel(p int) string {
	switch {
	case p <= 3:
		return LevelError
	case p == 4:
		return LevelWarn
	case p >= 7:
		return LevelDebug
	}
	return LevelInfo
}

// fold joins neighbours that differ only in their numbers (times, counters
// and IDs), keeping the newest. Oldest first, in and out.
func fold(lines []Line) []Line {
	out := make([]Line, 0, len(lines))
	last := ""
	for _, l := range lines {
		shape := digits.ReplaceAllString(l.Text, "#")
		if n := len(out); n > 0 && shape == last {
			c := max(out[n-1].Count, 1) + 1
			out[n-1] = l
			out[n-1].Count = c
			continue
		}
		out = append(out, l)
		last = shape
	}
	return out
}

// shape turns what was read into a column: search, fold, keep the newest.
func shape(lines []Line, re *regexp.Regexp) []Line {
	kept := make([]Line, 0, min(len(lines), MaxShown))
	for _, l := range lines {
		if re == nil || re.MatchString(l.Text) {
			kept = append(kept, l)
		}
	}
	kept = fold(kept)
	if len(kept) > MaxShown {
		kept = kept[len(kept)-MaxShown:]
	}
	return append(make([]Line, 0, len(kept)), kept...)
}

// container reads a container's newest lines. Docker's timestamp starts
// each line, since timestamps are asked for.
func (a *API) container(ctx context.Context, id string) ([]Line, bool, error) {
	var res broker.Logs
	if err := a.Broker.Call(ctx, "docker.containers.logs", map[string]any{"id": id, "tail": readLines, "timestamps": true}, &res); err != nil {
		return nil, false, err
	}
	out := make([]Line, 0, len(res.Lines))
	for _, l := range res.Lines {
		var ms int64
		text := l.Text
		if stamp, rest, ok := strings.Cut(text, " "); ok {
			if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				ms, text = t.UnixMilli(), rest
			}
		}
		text = clean(text)
		out = append(out, Line{Time: ms, Level: guessLevel(text), Text: text})
	}
	return out, res.Truncated, nil
}

// unit reads a unit's newest lines.
func (a *API) unit(ctx context.Context, id string) ([]Line, bool, error) {
	entries, truncated, err := services.ReadUnit(ctx, a.Run, id, readLines)
	if err != nil {
		return nil, false, err
	}
	out := make([]Line, 0, len(entries))
	for _, e := range entries {
		out = append(out, Line{Time: e.Time, Level: prioLevel(e.Prio), Text: clean(e.Msg)})
	}
	return out, truncated, nil
}

// read fills one column.
func (a *API) read(ctx context.Context, id string, re *regexp.Regexp) Source {
	s := Source{ID: id, Lines: []Line{}}
	var lines []Line
	var err error
	if c, ok := strings.CutPrefix(id, containerPrefix); ok {
		lines, s.Truncated, err = a.container(ctx, c)
	} else {
		lines, s.Truncated, err = a.unit(ctx, id)
	}
	if err != nil {
		s.Error = "these logs could not be read"
		if errors.Is(err, broker.ErrUnavailable) {
			s.Error = "container logs need hearth-broker, which is not running"
		}
		return s
	}
	s.Read = len(lines)
	s.Lines = shape(lines, re)
	return s
}

// parseQuery checks the sources and the pattern.
func parseQuery(sources []string, pattern string) (*regexp.Regexp, error) {
	if len(sources) < 1 || len(sources) > MaxSources {
		return nil, errors.New("choose 1 to 4 sources")
	}
	for i, s := range sources {
		if !ValidSource(s) {
			return nil, errors.New("source " + strconv.Itoa(i+1) + " is not a container or a unit")
		}
		for _, o := range sources[:i] {
			if o == s {
				return nil, errors.New("a source is listed twice")
			}
		}
	}
	if pattern == "" {
		return nil, nil
	}
	return alerts.CompilePattern(pattern)
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Handle serves GET /api/v1/logs?s=<source>&s=...&re=<RE2>.
func (a *API) Handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	re, err := parseQuery(q["s"], q.Get("re"))
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	select {
	case a.busy <- struct{}{}:
		defer func() { <-a.busy }()
	case <-r.Context().Done():
		return
	}
	out := make([]Source, 0, len(q["s"]))
	for _, id := range q["s"] {
		out = append(out, a.read(r.Context(), id, re))
	}
	reply(w, http.StatusOK, map[string]any{"sources": out})
}
