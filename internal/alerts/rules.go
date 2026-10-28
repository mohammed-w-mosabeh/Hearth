// Package alerts evaluates alert rules (SPEC 15) against the metrics
// hearthd already samples, the failed-units poller and the Docker monitor, records incidents
// and notifies the desktop once per incident.
package alerts

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Metric is something a rule can watch.
type Metric struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Unit  string `json:"unit"` // "%", "°C" or "" (a state)
	// Instance names what the metric has several of ("mount", "gpu",
	// "battery", "unit"); "" for a single value.
	Instance string `json:"instance,omitempty"`
	// Hysteresis is how far past the threshold, back toward normal, a
	// value must go before a firing alert resolves.
	Hysteresis float64 `json:"hysteresis"`
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
}

// Metrics is the catalog, in the order the UI lists it.
var Metrics = []Metric{
	{ID: "cpu.usage", Label: "CPU usage", Unit: "%", Hysteresis: 5, Max: 100},
	{ID: "mem.used_pct", Label: "Memory used", Unit: "%", Hysteresis: 5, Max: 100},
	{ID: "swap.used_pct", Label: "Swap used", Unit: "%", Hysteresis: 5, Max: 100},
	{ID: "disk.used_pct", Label: "Disk space used", Unit: "%", Instance: "mount", Hysteresis: 2, Max: 100},
	{ID: "cpu.temp", Label: "CPU temperature", Unit: "°C", Hysteresis: 5, Max: 150},
	{ID: "gpu.temp", Label: "GPU temperature", Unit: "°C", Instance: "gpu", Hysteresis: 5, Max: 150},
	{ID: "battery.discharging", Label: "Battery charge while unplugged", Unit: "%", Instance: "battery", Hysteresis: 3, Max: 100},
	{ID: "unit.failed", Label: "Unit failed", Instance: "unit", Max: 1},
	{ID: "container.exited", Label: "Container exited unexpectedly", Instance: "container", Max: 1},
	{ID: "job.failed", Label: "Scheduled job failed", Instance: "job", Max: 1},
}

func metric(id string) (Metric, bool) {
	for _, m := range Metrics {
		if m.ID == id {
			return m, true
		}
	}
	return Metric{}, false
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Validate checks a rule from the UI.
func Validate(r store.AlertRule) error {
	m, ok := metric(r.Metric)
	switch {
	case !ok:
		return fmt.Errorf("unknown metric %q", r.Metric)
	case strings.TrimSpace(r.Name) == "" || len(r.Name) > 80 || strings.ContainsFunc(r.Name, unicode.IsControl):
		return errors.New("name must be 1 to 80 characters")
	case len(r.Target) > 255 || strings.ContainsFunc(r.Target, unicode.IsControl):
		return errors.New("target must be at most 255 characters")
	case r.Target != "" && m.Instance == "":
		return fmt.Errorf("%s has no instances to pick", m.Label)
	case r.Op != ">" && r.Op != "<":
		return errors.New(`op must be ">" or "<"`)
	case math.IsNaN(r.Threshold) || r.Threshold < m.Min || r.Threshold > m.Max:
		return fmt.Errorf("threshold must be %g to %g", m.Min, m.Max)
	case r.ForSec < 0 || r.ForSec > 86400:
		return errors.New("duration must be 0 to 86400 seconds")
	case r.Severity != "info" && r.Severity != "warning" && r.Severity != "critical":
		return errors.New("severity must be info, warning or critical")
	case (r.QuietFrom == "") != (r.QuietTo == ""):
		return errors.New("quiet hours need both a start and an end")
	case r.QuietFrom != "" && (!hhmm.MatchString(r.QuietFrom) || !hhmm.MatchString(r.QuietTo) || r.QuietFrom == r.QuietTo):
		return errors.New(`quiet hours must be two different "HH:MM" times`)
	}
	return nil
}

// breached reports whether v is past the rule's threshold.
func breached(r store.AlertRule, v float64) bool {
	if r.Op == "<" {
		return v < r.Threshold
	}
	return v > r.Threshold
}

// cleared reports whether v is back past the threshold by the metric's
// hysteresis, so a firing alert resolves.
func cleared(r store.AlertRule, v float64) bool {
	m, _ := metric(r.Metric)
	if r.Op == "<" {
		return v >= r.Threshold+m.Hysteresis
	}
	return v <= r.Threshold-m.Hysteresis
}

// worse returns the more alarming of a and b for the rule.
func worse(r store.AlertRule, a, b float64) float64 {
	if r.Op == "<" {
		return math.Min(a, b)
	}
	return math.Max(a, b)
}

// minutes parses "HH:MM".
func minutes(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m
}

// quiet reports whether t (local time) is in the rule's quiet hours,
// which may wrap past midnight ("22:00" to "07:00").
func quiet(r store.AlertRule, t time.Time) bool {
	if r.QuietFrom == "" {
		return false
	}
	from, to, now := minutes(r.QuietFrom), minutes(r.QuietTo), t.Hour()*60+t.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to
}

// Describe is the notification text for an incident.
func Describe(e store.AlertEvent) (summary, body string) {
	m, _ := metric(e.Metric)
	if e.Metric == "unit.failed" {
		return e.RuleName, e.Instance + " has failed."
	}
	if e.Metric == "container.exited" {
		return e.RuleName, "Container " + e.Instance + " stopped unexpectedly."
	}
	if e.Metric == "job.failed" {
		return e.RuleName, "The scheduled job " + e.Instance + " failed."
	}
	val := func(v float64) string {
		if m.Unit == "%" {
			return strconv.FormatFloat(v, 'f', 0, 64) + "%"
		}
		return strconv.FormatFloat(v, 'f', 0, 64) + " " + m.Unit
	}
	body = m.Label
	if e.Instance != "" {
		body += " (" + e.Instance + ")"
	}
	body += " is " + val(e.Value) + ", " + map[string]string{">": "above", "<": "below"}[e.Op] + " " + val(e.Threshold) + "."
	return e.RuleName, body
}
