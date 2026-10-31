// Package bootinfo is boot analysis (SPEC 18, ROADMAP 4.9): how long the
// last boot took and why, from systemd-analyze. It only reads.
package bootinfo

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// maxBlame bounds the units listed.
const maxBlame = 40

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Phase is one stage of the boot.
type Phase struct {
	Name string `json:"name"` // firmware, loader, kernel, initrd, userspace
	Ms   int64  `json:"ms"`
}

// Unit is a unit and how long it took to start.
type Unit struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

// Link is one step of the critical chain, the target first.
type Link struct {
	Name string `json:"name"`
	AtMs int64  `json:"at_ms"` // when it became active
	Ms   int64  `json:"ms"`    // how long it took; 0 when it took no time
}

// Report is GET /api/v1/boot.
type Report struct {
	// Finished is false while the boot has not ended (a unit still
	// starting); the times then cannot be known.
	Finished bool    `json:"finished"`
	TotalMs  int64   `json:"total_ms"`
	Phases   []Phase `json:"phases"`
	Target   string  `json:"target,omitempty"`
	// TargetMs is when the desktop or login was ready, after userspace began.
	TargetMs int64  `json:"target_ms,omitempty"`
	Blame    []Unit `json:"blame"`
	Chain    []Link `json:"chain"`
	Error    string `json:"error,omitempty"`
}

var (
	spanRe   = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*(y|month|w|d|h|min|ms|us|µs|s)\b`)
	phaseRe  = regexp.MustCompile(`((?:[0-9.]+\s*[a-zµ]+\s*)+)\((firmware|loader|kernel|initrd|userspace)\)`)
	totalRe  = regexp.MustCompile(`=\s*((?:[0-9.]+\s*[a-zµ]+\s*)+)$`)
	targetRe = regexp.MustCompile(`^(\S+) reached after (.+) in userspace`)
	chainRe  = regexp.MustCompile(`^[\s│├└─]*(\S+?)(?: @([^+]+?))?(?: \+(.+))?\s*$`)
	unitChar = regexp.MustCompile(`\.(service|target|socket|mount|device|timer|path|slice|scope|swap)$`)
)

var unitMs = map[string]float64{"y": 31557600e3, "month": 2629800e3, "w": 604800e3, "d": 86400e3, "h": 3600e3, "min": 60e3, "s": 1e3, "ms": 1, "us": 1e-3, "µs": 1e-3}

// span reads a systemd time span ("2min 6.668s", "189ms") as milliseconds.
func span(s string) int64 {
	var ms float64
	for _, m := range spanRe.FindAllStringSubmatch(s, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			ms += v * unitMs[m[2]]
		}
	}
	return int64(ms + 0.5)
}

// parseTime reads systemd-analyze time.
func parseTime(text string, r *Report) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Startup finished") {
			r.Finished = true
			for _, m := range phaseRe.FindAllStringSubmatch(line, -1) {
				r.Phases = append(r.Phases, Phase{Name: m[2], Ms: span(m[1])})
			}
			if m := totalRe.FindStringSubmatch(line); m != nil {
				r.TotalMs = span(m[1])
			}
		} else if m := targetRe.FindStringSubmatch(line); m != nil {
			r.Target, r.TargetMs = m[1], span(m[2])
		}
	}
}

// parseBlame reads systemd-analyze blame, slowest first: the span, then
// the unit, whose name has no spaces.
func parseBlame(text string) []Unit {
	out := []Unit{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name := f[len(f)-1]
		if ms := span(strings.Join(f[:len(f)-1], " ")); unitChar.MatchString(name) {
			out = append(out, Unit{Name: name, Ms: ms})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ms > out[j].Ms })
	return out[:min(len(out), maxBlame)]
}

// parseChain reads systemd-analyze critical-chain.
func parseChain(text string) []Link {
	out := []Link{}
	for _, line := range strings.Split(text, "\n") {
		m := chainRe.FindStringSubmatch(line)
		if m == nil || !unitChar.MatchString(m[1]) {
			continue
		}
		out = append(out, Link{Name: m[1], AtMs: span(m[2]), Ms: span(m[3])})
	}
	return out
}

// Read runs the analysis.
func Read(ctx context.Context, run Runner) Report {
	r := Report{Phases: []Phase{}, Blame: []Unit{}, Chain: []Link{}}
	res, err := run(ctx, "analyze-time", "time", "--no-pager")
	if err != nil {
		r.Error = "systemd-analyze could not run"
		return r
	}
	parseTime(string(res.Stdout), &r)
	if !r.Finished {
		return r // blame and the chain would only fail the same way
	}
	if res, err := run(ctx, "analyze-blame", "blame", "--no-pager"); err == nil {
		r.Blame = parseBlame(string(res.Stdout))
	}
	if res, err := run(ctx, "analyze-chain", "critical-chain", "--no-pager"); err == nil {
		r.Chain = parseChain(string(res.Stdout))
	}
	return r
}

// Handle serves GET /api/v1/boot.
func Handle(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(Read(r.Context(), run))
	}
}
