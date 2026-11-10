// Package spec is an automation's definition and the one validator every
// way in uses: the editor, templates, import and the migrations (SECURITY
// S15d, docs/plan/automations.md). It does no I/O, so it can be fuzzed;
// what depends on the machine (a target exists, an action is registered,
// a schedule parses) is checked again by the engine at save and at run.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Bounds.
const (
	MaxSpec        = 16 << 10
	MaxConditions  = 8
	MaxSteps       = 16
	MaxBranch      = 8
	MaxParams      = 16
	MaxName        = 80
	MaxText        = 300
	MaxWaitStep    = 600
	MaxWaitRun     = 900
	MaxValue       = 4096
	DefaultCool    = 300
	DefaultPerHour = 6
)

// Spec is WHEN, IF, THEN and the options.
type Spec struct {
	Trigger Trigger     `json:"trigger"`
	If      *Conditions `json:"if,omitempty"`
	Then    []Step      `json:"then"`
	Options Options     `json:"options"`
}

// Trigger is one of the closed kinds; only the fields of its kind may be
// set (decodeKind enforces it).
type Trigger struct {
	Kind      string  `json:"kind"`
	Calendar  string  `json:"calendar,omitempty"`  // schedule
	Metric    string  `json:"metric,omitempty"`    // metric
	Op        string  `json:"op,omitempty"`        // metric
	Threshold float64 `json:"threshold,omitempty"` // metric
	ForS      int     `json:"for_s,omitempty"`     // metric
	Target    string  `json:"target,omitempty"`    // metric instance, container, unit, integrity item, stack
	Scope     string  `json:"scope,omitempty"`     // unit_failed
	Event     string  `json:"event,omitempty"`     // container, power, display, files
	State     string  `json:"state,omitempty"`     // wifi, vpn, internet, app
	UUID      string  `json:"uuid,omitempty"`      // wifi, vpn
	Count     int     `json:"count,omitempty"`     // ssh_failed
	Minutes   int     `json:"minutes,omitempty"`   // idle
	Game      bool    `json:"game,omitempty"`      // app
	App       string  `json:"app,omitempty"`       // app
	Folder    string  `json:"folder,omitempty"`    // files: "~/..."
	Days      int     `json:"days,omitempty"`      // files older_than
	Depth     int     `json:"depth,omitempty"`     // files
	Pattern   string  `json:"pattern,omitempty"`   // files
	Severity  string  `json:"severity,omitempty"`  // alert
}

// Conditions combine with all or any, one level deep.
type Conditions struct {
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
}

// Condition is one closed kind.
type Condition struct {
	Kind    string   `json:"kind"`
	From    string   `json:"from,omitempty"`    // time
	To      string   `json:"to,omitempty"`      // time
	Days    []string `json:"days,omitempty"`    // weekday
	On      string   `json:"on,omitempty"`      // power
	Op      string   `json:"op,omitempty"`      // battery
	Percent int      `json:"percent,omitempty"` // battery
	UUID    string   `json:"uuid,omitempty"`    // wifi
	Mode    string   `json:"mode,omitempty"`    // mode
	Is      bool     `json:"is,omitempty"`      // wifi connected, mode active, game running
	Target  string   `json:"target,omitempty"`  // container, unit
	Scope   string   `json:"scope,omitempty"`   // unit
	State   string   `json:"state,omitempty"`   // container, unit
}

// Step is an action, notify, wait or check.
type Step struct {
	Step     string            `json:"step"`
	Action   string            `json:"action,omitempty"`   // action: "kind/name"
	Target   string            `json:"target,omitempty"`   // action, check
	Params   map[string]string `json:"params,omitempty"`   // action
	Continue bool              `json:"continue,omitempty"` // action: go on after a failure
	Label    string            `json:"label,omitempty"`
	Channels []string          `json:"channels,omitempty"` // notify
	Text     string            `json:"text,omitempty"`     // notify
	Severity string            `json:"severity,omitempty"` // notify: info (default), warning, critical
	Seconds  int               `json:"seconds,omitempty"`  // wait
	What     string            `json:"what,omitempty"`     // check
	Expect   string            `json:"expect,omitempty"`   // check
	ThenDo   []Step            `json:"then,omitempty"`     // check
	ElseDo   []Step            `json:"else,omitempty"`     // check
}

// Options bound how often it runs.
type Options struct {
	CooldownS   int    `json:"cooldown_s"`
	MaxPerHour  int    `json:"max_per_hour"`
	QuietFrom   string `json:"quiet_from,omitempty"`
	QuietTo     string `json:"quiet_to,omitempty"`
	RunIfMissed bool   `json:"run_if_missed,omitempty"` // schedule only (Q3)
}

// Fields each kind may set besides "kind" (or "step").
var triggerFields = map[string][]string{
	"schedule":          {"calendar"},
	"login":             nil,
	"metric":            {"metric", "op", "threshold", "for_s", "target"},
	"alert":             {"metric", "op", "threshold", "for_s", "target", "severity"},
	"container":         {"event", "target"},
	"unit_failed":       {"scope", "target"},
	"new_listener":      nil,
	"usb_device":        nil,
	"security_updates":  nil,
	"image_update":      {"target"},
	"integrity_changed": {"target"},
	"ssh_failed":        {"count"},
	"power":             {"event"},
	"wifi":              {"uuid", "state"},
	"vpn":               {"uuid", "state"},
	"internet":          {"state"},
	"idle":              {"minutes"},
	"resume":            nil,
	"display":           {"event"},
	"app":               {"state", "game", "app"},
	"files":             {"folder", "event", "days", "depth", "pattern"},
}

var conditionFields = map[string][]string{
	"time":      {"from", "to"},
	"weekday":   {"days"},
	"power":     {"on"},
	"battery":   {"op", "percent"},
	"wifi":      {"uuid", "is"},
	"mode":      {"mode", "is"},
	"game":      {"is"},
	"container": {"target", "state"},
	"unit":      {"scope", "target", "state"},
}

var stepFields = map[string][]string{
	"action": {"action", "target", "params", "continue", "label"},
	"notify": {"channels", "text", "label", "severity"},
	"wait":   {"seconds", "label"},
	"check":  {"what", "target", "expect", "then", "else", "label"},
}

// Metrics a metric trigger may watch, with the range its threshold may
// take (alert metric IDs, internal/alerts).
var Metrics = map[string][2]float64{
	"cpu.usage":           {0, 100},
	"mem.used_pct":        {0, 100},
	"swap.used_pct":       {0, 100},
	"disk.used_pct":       {0, 100},
	"cpu.temp":            {0, 150},
	"gpu.temp":            {0, 150},
	"battery.discharging": {0, 100},
}

// Placeholders a notification's text may hold.
var Placeholders = []string{"automation", "trigger", "instance", "value", "top", "journal", "outage", "sources", "alert"}

// Denied are actions an automation may never hold: their reach is too
// wide for a run confirmed later (docs/plan/automations.md §4).
var Denied = []string{"hearth/import", "uninstall/remove", "uninstall/restore", "package/update-all", "space/empty-trash", "job/delete",
	"automation/approve", "automation/dismiss", "mode/approve", "mode/dismiss", "stack/grant"}

var (
	clockRe    = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	actionRe   = regexp.MustCompile(`^[a-z][a-z-]{0,30}/[a-z][a-z-]{0,30}$`)
	paramRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	instanceRe = regexp.MustCompile(`^[A-Za-z0-9_.@:/ +-]{1,200}$`)
	appRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+~@-]{0,199}$`)
	modeRe     = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	patternRe  = regexp.MustCompile(`^\*(\.[A-Za-z0-9]{1,10})?$`)
	folderRe   = regexp.MustCompile(`^~(/[A-Za-z0-9_][A-Za-z0-9_. ()+-]{0,127}){0,6}$`)
	calRe      = regexp.MustCompile(`^[A-Za-z0-9 :*,./~-]{1,128}$`)
	holeRe     = regexp.MustCompile(`\{([a-z]*)\}`)
)

func oneOf(v string, vs ...string) bool { return slices.Contains(vs, v) }

// clean reports whether s is valid UTF-8 with no control character.
func clean(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// CheckName checks an automation's name.
func CheckName(s string) error {
	if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > MaxName || !clean(s) {
		return fmt.Errorf("name must be 1 to %d characters, no control characters", MaxName)
	}
	return nil
}

// decodeKind decodes raw into v strictly, and refuses any key that is not
// "kind" (or "step") or one of the kind's own fields.
func decodeKind(raw json.RawMessage, key string, fields map[string][]string, v any) (string, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return "", errors.New("must be an object")
	}
	var kind string
	if err := json.Unmarshal(keys[key], &kind); err != nil {
		return "", fmt.Errorf("%s is required", key)
	}
	allowed, ok := fields[kind]
	if !ok {
		return "", fmt.Errorf("unknown %s %q", key, clip(kind))
	}
	for k := range keys {
		if k != key && !slices.Contains(allowed, k) {
			return "", fmt.Errorf("%s %s takes no %q", key, kind, clip(k))
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return "", fmt.Errorf("%s %s: %w", key, kind, err)
	}
	return kind, nil
}

func clip(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// wire is the spec as it arrives, its parts kept raw for decodeKind.
type wire struct {
	Trigger json.RawMessage `json:"trigger"`
	If      *struct {
		Match      string            `json:"match"`
		Conditions []json.RawMessage `json:"conditions"`
	} `json:"if,omitempty"`
	Then    []json.RawMessage `json:"then"`
	Options json.RawMessage   `json:"options"`
}

// Parse decodes and validates a spec. Anything unknown, out of bounds or of
// the wrong shape refuses the whole spec.
func Parse(b []byte) (Spec, error) {
	var s Spec
	if len(b) > MaxSpec {
		return s, errors.New("spec too large")
	}
	var w wire
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return s, fmt.Errorf("spec: %w", err)
	}
	if dec.More() {
		return s, errors.New("spec: trailing data")
	}
	if len(w.Trigger) == 0 {
		return s, errors.New("a trigger is required")
	}
	if _, err := decodeKind(w.Trigger, "kind", triggerFields, &s.Trigger); err != nil {
		return Spec{}, fmt.Errorf("trigger: %w", err)
	}
	if w.If != nil {
		s.If = &Conditions{Match: w.If.Match}
		if len(w.If.Conditions) > MaxConditions {
			return Spec{}, fmt.Errorf("at most %d conditions", MaxConditions)
		}
		for i, raw := range w.If.Conditions {
			var c Condition
			if _, err := decodeKind(raw, "kind", conditionFields, &c); err != nil {
				return Spec{}, fmt.Errorf("condition %d: %w", i+1, err)
			}
			s.If.Conditions = append(s.If.Conditions, c)
		}
	}
	steps, err := parseSteps(w.Then, true)
	if err != nil {
		return Spec{}, err
	}
	s.Then = steps
	if len(w.Options) > 0 {
		dec := json.NewDecoder(bytes.NewReader(w.Options))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s.Options); err != nil {
			return Spec{}, fmt.Errorf("options: %w", err)
		}
	} else {
		s.Options = Options{CooldownS: DefaultCool, MaxPerHour: DefaultPerHour}
	}
	return s, s.Check()
}

func parseSteps(raws []json.RawMessage, top bool) ([]Step, error) {
	limit := MaxSteps
	if !top {
		limit = MaxBranch
	}
	if len(raws) > limit {
		return nil, fmt.Errorf("at most %d steps", limit)
	}
	var out []Step
	for i, raw := range raws {
		var probe struct {
			Then []json.RawMessage `json:"then"`
			Else []json.RawMessage `json:"else"`
		}
		var st Step
		kind, err := decodeKind(raw, "step", stepFields, &st)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		if kind == "check" {
			if !top {
				return nil, fmt.Errorf("step %d: a check cannot hold another check", i+1)
			}
			_ = json.Unmarshal(raw, &probe)
			if st.ThenDo, err = parseSteps(probe.Then, false); err != nil {
				return nil, fmt.Errorf("step %d then: %w", i+1, err)
			}
			if st.ElseDo, err = parseSteps(probe.Else, false); err != nil {
				return nil, fmt.Errorf("step %d else: %w", i+1, err)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// Check validates a spec already decoded (templates, migrations).
func (s Spec) Check() error {
	if err := s.Trigger.check(); err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	if s.If != nil {
		if !oneOf(s.If.Match, "all", "any") || len(s.If.Conditions) == 0 || len(s.If.Conditions) > MaxConditions {
			return fmt.Errorf("if: match must be all or any, with 1 to %d conditions", MaxConditions)
		}
		for i, c := range s.If.Conditions {
			if err := c.check(); err != nil {
				return fmt.Errorf("condition %d: %w", i+1, err)
			}
		}
	}
	if len(s.Then) == 0 || len(s.Then) > MaxSteps {
		return fmt.Errorf("then: 1 to %d steps", MaxSteps)
	}
	wait, err := checkSteps(s.Then, true)
	if err != nil {
		return err
	}
	if wait > MaxWaitRun {
		return fmt.Errorf("a run may wait %d seconds at most", MaxWaitRun)
	}
	return s.Options.check(s.Trigger.Kind)
}

func (t Trigger) check() error {
	bad := func(f string, a ...any) error { return fmt.Errorf(f, a...) }
	switch t.Kind {
	case "schedule":
		if !calRe.MatchString(t.Calendar) {
			return bad("calendar must be an OnCalendar expression")
		}
	case "alert":
		// An incident of an alert rule opens (ROADMAP 12.7): the alert
		// engine evaluates it, with the Alerts page's own checks.
		if err := alerts.Validate(store.AlertRule{Name: "rule", Metric: t.Metric, Target: t.Target, Op: t.Op,
			Threshold: t.Threshold, ForSec: t.ForS, Severity: t.Severity}); err != nil {
			return bad("%s", err.Error())
		}
	case "metric":
		r, ok := Metrics[t.Metric]
		if !ok {
			return bad("unknown metric")
		}
		if !oneOf(t.Op, ">", "<") || t.Threshold < r[0] || t.Threshold > r[1] || t.ForS < 0 || t.ForS > 86400 {
			return bad("op must be > or <, threshold %g to %g, for_s 0 to 86400", r[0], r[1])
		}
		if t.Target != "" && !instanceRe.MatchString(t.Target) {
			return bad("bad target")
		}
	case "container":
		if !oneOf(t.Event, "died", "unhealthy", "oom") || (t.Target != "" && !instanceRe.MatchString(t.Target)) {
			return bad("event must be died, unhealthy or oom")
		}
	case "unit_failed":
		if !oneOf(t.Scope, "", "user", "system") || (t.Target != "" && !unitRe.MatchString(t.Target)) {
			return bad("scope must be user or system, target a unit name")
		}
	case "image_update", "integrity_changed":
		if t.Target != "" && !instanceRe.MatchString(t.Target) {
			return bad("bad target")
		}
	case "ssh_failed":
		if t.Count < 3 || t.Count > 1000 {
			return bad("count must be 3 to 1000 an hour")
		}
	case "power":
		if !oneOf(t.Event, "plugged", "unplugged") {
			return bad("event must be plugged or unplugged")
		}
	case "wifi", "vpn":
		if !uuidRe.MatchString(t.UUID) || !oneOf(t.State, "up", "down") {
			return bad("a connection UUID and up or down")
		}
	case "internet":
		if !oneOf(t.State, "down", "back") {
			return bad("state must be down or back")
		}
	case "idle":
		if t.Minutes < 5 || t.Minutes > 240 {
			return bad("minutes must be 5 to 240")
		}
	case "display":
		if !oneOf(t.Event, "connected", "disconnected") {
			return bad("event must be connected or disconnected")
		}
	case "app":
		if !oneOf(t.State, "started", "stopped") || t.Game == (t.App != "") || (t.App != "" && !appRe.MatchString(t.App)) {
			return bad("state started or stopped, and either game or one app")
		}
	case "files":
		if !folderRe.MatchString(t.Folder) || strings.Contains(t.Folder, "/..") || strings.Contains(t.Folder, "/./") ||
			!oneOf(t.Event, "new", "older_than") || t.Depth < 0 || t.Depth > 2 || !patternRe.MatchString(t.Pattern) {
			return bad("a folder under ~, new or older_than, depth 0 to 2, a pattern like *.pdf")
		}
		if (t.Event == "older_than") != (t.Days > 0) || t.Days > 3650 {
			return bad("days (1 to 3650) only with older_than")
		}
	case "login", "new_listener", "usb_device", "security_updates", "resume":
	default:
		return bad("unknown kind")
	}
	return nil
}

var unitRe = regexp.MustCompile(`^[A-Za-z0-9:_.\\-]{1,250}\.(service|socket|timer|path|mount|target)$`)

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func (c Condition) check() error {
	switch c.Kind {
	case "time":
		if !clockRe.MatchString(c.From) || !clockRe.MatchString(c.To) || c.From == c.To {
			return errors.New("from and to are HH:MM, different")
		}
	case "weekday":
		if len(c.Days) == 0 || len(c.Days) > 7 {
			return errors.New("1 to 7 days")
		}
		for i, d := range c.Days {
			if !slices.Contains(weekdays, d) || slices.Contains(c.Days[:i], d) {
				return errors.New("days are mon to sun, each once")
			}
		}
	case "power":
		if !oneOf(c.On, "ac", "battery") {
			return errors.New("on must be ac or battery")
		}
	case "battery":
		if !oneOf(c.Op, "above", "below") || c.Percent < 1 || c.Percent > 99 {
			return errors.New("above or below 1 to 99 %")
		}
	case "wifi":
		if !uuidRe.MatchString(c.UUID) {
			return errors.New("a connection UUID")
		}
	case "mode":
		if !modeRe.MatchString(c.Mode) {
			return errors.New("a mode ID")
		}
	case "game":
	case "container":
		if !instanceRe.MatchString(c.Target) || !oneOf(c.State, "running", "stopped", "healthy", "unhealthy") {
			return errors.New("a container and running, stopped, healthy or unhealthy")
		}
	case "unit":
		if !oneOf(c.Scope, "user", "system") || !unitRe.MatchString(c.Target) || !oneOf(c.State, "active", "inactive", "failed") {
			return errors.New("a scope, a unit and active, inactive or failed")
		}
	default:
		return errors.New("unknown kind")
	}
	return nil
}

// checkSteps validates steps and returns the longest wait a run of them
// can take.
func checkSteps(steps []Step, top bool) (int, error) {
	wait := 0
	for i, st := range steps {
		if st.Label != "" && (utf8.RuneCountInString(st.Label) > MaxName || !clean(st.Label)) {
			return 0, fmt.Errorf("step %d: label must be %d characters at most", i+1, MaxName)
		}
		switch st.Step {
		case "action":
			if err := checkAction(st); err != nil {
				return 0, fmt.Errorf("step %d: %w", i+1, err)
			}
		case "notify":
			if err := CheckNotify(st); err != nil {
				return 0, fmt.Errorf("step %d: %w", i+1, err)
			}
		case "wait":
			if st.Seconds < 1 || st.Seconds > MaxWaitStep {
				return 0, fmt.Errorf("step %d: wait 1 to %d seconds", i+1, MaxWaitStep)
			}
			wait += st.Seconds
		case "check":
			if !top {
				return 0, fmt.Errorf("step %d: a check cannot hold another check", i+1)
			}
			if !oneOf(st.What, "container_health", "container_state", "unit_state") || !instanceRe.MatchString(st.Target) ||
				!oneOf(st.Expect, "healthy", "unhealthy", "running", "stopped", "active", "inactive", "failed") {
				return 0, fmt.Errorf("step %d: check what, target and expect", i+1)
			}
			if len(st.ThenDo) > MaxBranch || len(st.ElseDo) > MaxBranch || len(st.ThenDo)+len(st.ElseDo) == 0 {
				return 0, fmt.Errorf("step %d: then and else hold 0 to %d steps, one at least", i+1, MaxBranch)
			}
			a, err := checkSteps(st.ThenDo, false)
			if err != nil {
				return 0, fmt.Errorf("step %d then: %w", i+1, err)
			}
			b, err := checkSteps(st.ElseDo, false)
			if err != nil {
				return 0, fmt.Errorf("step %d else: %w", i+1, err)
			}
			wait += max(a, b)
		default:
			return 0, fmt.Errorf("step %d: unknown step", i+1)
		}
	}
	return wait, nil
}

func checkAction(st Step) error {
	if !actionRe.MatchString(st.Action) {
		return errors.New("action must be kind/name")
	}
	if slices.Contains(Denied, st.Action) {
		return fmt.Errorf("%s is never allowed in an automation", st.Action)
	}
	if len(st.Target) > MaxValue || !clean(st.Target) || strings.HasPrefix(st.Target, "-") {
		return errors.New("bad target")
	}
	if len(st.Params) > MaxParams {
		return fmt.Errorf("at most %d params", MaxParams)
	}
	for k, v := range st.Params {
		if !paramRe.MatchString(k) || len(v) > MaxValue || !clean(v) {
			return errors.New("bad param")
		}
	}
	return nil
}

// CheckNotify validates a notify step (also notify/send's request).
func CheckNotify(st Step) error {
	if len(st.Channels) == 0 || len(st.Channels) > 3 {
		return errors.New("1 to 3 channels")
	}
	// "all" is the desktop and every remote channel that is turned on, as
	// alerts have always been sent; it stands alone.
	if slices.Contains(st.Channels, "all") && len(st.Channels) != 1 {
		return errors.New(`"all" stands alone`)
	}
	for i, c := range st.Channels {
		if !oneOf(c, "desktop", "ntfy", "mail", "all") || slices.Contains(st.Channels[:i], c) {
			return errors.New("channels are desktop, ntfy and mail, each once, or all")
		}
	}
	if !oneOf(st.Severity, "", "info", "warning", "critical") {
		return errors.New("severity must be info, warning or critical")
	}
	if strings.TrimSpace(st.Text) == "" || utf8.RuneCountInString(st.Text) > MaxText || !clean(st.Text) {
		return fmt.Errorf("text must be 1 to %d characters", MaxText)
	}
	// Braces only around a known placeholder.
	rest := holeRe.ReplaceAllStringFunc(st.Text, func(m string) string {
		if slices.Contains(Placeholders, m[1:len(m)-1]) {
			return ""
		}
		return m
	})
	if strings.ContainsAny(rest, "{}") {
		return errors.New("text holds an unknown placeholder")
	}
	return nil
}

func (o Options) check(trigger string) error {
	if o.CooldownS < 60 || o.CooldownS > 86400 || o.MaxPerHour < 1 || o.MaxPerHour > 60 {
		return errors.New("cooldown 60 to 86400 seconds, 1 to 60 runs an hour")
	}
	if (o.QuietFrom == "") != (o.QuietTo == "") ||
		(o.QuietFrom != "" && (!clockRe.MatchString(o.QuietFrom) || !clockRe.MatchString(o.QuietTo) || o.QuietFrom == o.QuietTo)) {
		return errors.New("quiet hours are two different HH:MM, or none")
	}
	if o.RunIfMissed && trigger != "schedule" {
		return errors.New("run_if_missed is for schedules only")
	}
	return nil
}

// Steps visits every step, branches included.
func (s Spec) Steps(fn func(Step)) {
	for _, st := range s.Then {
		fn(st)
		for _, b := range st.ThenDo {
			fn(b)
		}
		for _, b := range st.ElseDo {
			fn(b)
		}
	}
}
