package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const (
	logindDest    = "org.freedesktop.login1"
	logindPath    = dbus.ObjectPath("/org/freedesktop/login1")
	listInhibit   = "org.freedesktop.login1.Manager.ListInhibitors"
	enableCharge  = deviceIface + ".EnableChargeThreshold"
	sleepDays     = 30
	maxInhibitors = 64
	maxSleepText  = 255
)

// Inhibitor is something holding sleep or idle back. Who and why come from
// the program that took it: plain text, escaped by the UI.
type Inhibitor struct {
	What string `json:"what"` // sleep, idle, shutdown, handle-lid-switch...
	Who  string `json:"who"`
	Why  string `json:"why"`
	Mode string `json:"mode"` // block or delay
	UID  uint32 `json:"uid"`
	PID  uint32 `json:"pid"`
}

// Sleep is one stretch asleep; To is 0 while it has no recorded end.
type Sleep struct {
	From int64  `json:"from"` // unix ms
	To   int64  `json:"to,omitempty"`
	Mode string `json:"mode"` // suspend, hibernate...
}

// ChargeLimit is one battery's charge threshold.
type ChargeLimit struct {
	Battery string `json:"battery"`
	Via     string `json:"via"`           // "kernel" or "upower"
	End     int    `json:"end,omitempty"` // percent; upower: while enabled
	Enabled bool   `json:"enabled"`
}

// SleepReport is GET /api/v1/power/sleep.
type SleepReport struct {
	Inhibitors []Inhibitor   `json:"inhibitors"`
	Sleeps     []Sleep       `json:"sleeps"` // newest first
	Limits     []ChargeLimit `json:"limits"`
	// Limited: the system journal is out of this user's reach, so the
	// history is missing.
	Limited bool `json:"limited,omitempty"`
}

func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if len(s) > maxSleepText {
		s = strings.ToValidUTF8(s[:maxSleepText], "")
	}
	return s
}

// parseInhibitors keeps what can stop sleep or idle.
func parseInhibitors(rows [][]any) []Inhibitor {
	out := []Inhibitor{}
	for _, r := range rows {
		if len(r) != 6 || len(out) == maxInhibitors {
			continue
		}
		what, ok1 := r[0].(string)
		who, ok2 := r[1].(string)
		why, ok3 := r[2].(string)
		mode, ok4 := r[3].(string)
		uid, ok5 := r[4].(uint32)
		pid, ok6 := r[5].(uint32)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
			continue
		}
		parts := strings.Split(what, ":")
		if !slices.Contains(parts, "sleep") && !slices.Contains(parts, "idle") {
			continue
		}
		out = append(out, Inhibitor{What: cleanText(what), Who: cleanText(who), Why: cleanText(why), Mode: cleanText(mode), UID: uid, PID: pid})
	}
	return out
}

var sleepMode = regexp.MustCompile(`^[a-z-]{1,32}$`)

// parseSleeps pairs the journal's entries, newest first, into stretches.
func parseSleeps(b []byte) []Sleep {
	out := []Sleep{}
	wake := int64(0) // a left entry waiting for its entered entry
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		var e struct {
			ID    string `json:"MESSAGE_ID"`
			Mode  string `json:"SLEEP"`
			Stamp string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		us, err := strconv.ParseInt(e.Stamp, 10, 64)
		if err != nil || us <= 0 {
			continue
		}
		switch e.ID {
		case safeexec.SleepLeftID:
			wake = us / 1000
		case safeexec.SleepEnteredID:
			mode := e.Mode
			if !sleepMode.MatchString(mode) {
				mode = "sleep"
			}
			out = append(out, Sleep{From: us / 1000, To: wake, Mode: mode})
			wake = 0
		}
	}
	return out
}

// SleepConfig configures a SleepAPI.
type SleepConfig struct {
	W             *Watcher
	Run           func(ctx context.Context, name string, args ...string) (safeexec.Result, error)
	CanReadSystem func() bool
	Broker        interface {
		Call(ctx context.Context, method string, params, out any) error
	}
	SysRoot string // default /sys
	Now     func() time.Time
}

// SleepAPI serves sleep and the charge limit (ROADMAP 6.4).
type SleepAPI struct{ cfg SleepConfig }

// NewSleep returns a SleepAPI.
func NewSleep(cfg SleepConfig) *SleepAPI {
	if cfg.SysRoot == "" {
		cfg.SysRoot = broker.SysRoot
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &SleepAPI{cfg: cfg}
}

func (a *SleepAPI) inhibitors(ctx context.Context) []Inhibitor {
	a.cfg.W.mu.Lock()
	bus := a.cfg.W.bus
	a.cfg.W.mu.Unlock()
	if bus == nil {
		return []Inhibitor{}
	}
	var rows [][]any
	if err := bus.Call(ctx, logindDest, logindPath, listInhibit, []any{&rows}); err != nil {
		a.cfg.W.log.Debug("inhibitors", "err", err)
		return []Inhibitor{}
	}
	return parseInhibitors(rows)
}

// kernelLimit reads one battery's kernel limit.
func (a *SleepAPI) kernelLimit(id string) (int, bool) {
	if !broker.ValidBatteryName(id) {
		return 0, false
	}
	b, err := os.ReadFile(filepath.Join(a.cfg.SysRoot, "class", "power_supply", id, broker.ChargeEndFile)) // #nosec G304 -- a validated name under sysfs
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil
}

// limits lists the batteries whose charge can be limited, UPower first.
func (a *SleepAPI) limits() []ChargeLimit {
	out := []ChargeLimit{}
	for _, b := range a.cfg.W.Snapshot().Batteries {
		if b.limitSupported {
			out = append(out, ChargeLimit{Battery: b.ID, Via: "upower", End: b.limitEnd, Enabled: b.limitEnabled})
		} else if n, ok := a.kernelLimit(b.ID); ok {
			out = append(out, ChargeLimit{Battery: b.ID, Via: "kernel", End: n, Enabled: n < broker.MaxChargeEnd})
		}
	}
	return out
}

// Read builds the report.
func (a *SleepAPI) Read(ctx context.Context) SleepReport {
	r := SleepReport{Inhibitors: a.inhibitors(ctx), Sleeps: []Sleep{}, Limits: a.limits()}
	since := a.cfg.Now().Add(-sleepDays * 24 * time.Hour).Unix()
	if res, err := a.cfg.Run(ctx, "journalctl-sleep", safeexec.SleepArgs(since)...); err == nil || res.ExitCode == 1 {
		r.Sleeps = parseSleeps(res.Stdout)
	}
	if a.cfg.CanReadSystem != nil {
		r.Limited = !a.cfg.CanReadSystem()
	}
	return r
}

// Handle serves GET /api/v1/power/sleep.
func (a *SleepAPI) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(a.Read(r.Context()))
}

// validLimit checks battery/threshold's request.
func validLimit(r actions.Request) (int, error) {
	end, err := strconv.Atoi(r.Params["end"])
	if !broker.ValidBatteryName(r.Target) || len(r.Params) != 1 || err != nil ||
		end < broker.MinChargeEnd || end > broker.MaxChargeEnd || r.Params["end"] != strconv.Itoa(end) {
		return 0, errors.New(`target must be a battery and params {"end": "50".."100"}`)
	}
	return end, nil
}

// setLimit applies the limit through UPower, which has its own polkit
// action, or else the broker. UPower has one limit of its own: on or off.
func (a *SleepAPI) setLimit(ctx context.Context, id string, end int) error {
	for path, b := range a.cfg.W.batteriesByPath() {
		if b.ID == id && b.limitSupported {
			a.cfg.W.mu.Lock()
			bus := a.cfg.W.bus
			a.cfg.W.mu.Unlock()
			if bus == nil {
				return errors.New("UPower is not available")
			}
			return bus.Call(ctx, upowerDest, path, enableCharge, nil, end < broker.MaxChargeEnd)
		}
	}
	return a.cfg.Broker.Call(ctx, "battery.threshold", broker.ThresholdParams{Battery: id, End: end}, nil)
}

// Register adds battery/threshold. It is Safe in the registry's terms: it
// changes one reversible limit, and polkit asks before it does.
func (a *SleepAPI) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "battery", Name: "threshold", Risk: actions.Safe,
		Validate: func(r actions.Request) error { _, err := validLimit(r); return err },
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "limit charging of " + r.Target + " to " + r.Params["end"] + "%", Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
			end, err := validLimit(r)
			if err != nil {
				return res, err
			}
			err = a.setLimit(ctx, r.Target, end)
			it := p.Items[0]
			var be *broker.Error
			switch {
			case err == nil:
				res.Done = 1
			case errors.Is(err, broker.ErrUnauthorized):
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: "unauthorized"})
			case errors.Is(err, broker.ErrUnavailable):
				return res, actions.Errorf(http.StatusServiceUnavailable, "hearth-broker is not available")
			case errors.Is(err, broker.ErrNotMember):
				return res, actions.Errorf(http.StatusForbidden, "you are not in the hearth group")
			case errors.As(err, &be):
				it.Detail = be.Message
				res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: "error"})
			default:
				return res, actions.Errorf(http.StatusBadGateway, "the charge limit could not be set")
			}
			return res, nil
		}})
}

// Sleeps lists the sleeps since the given time, oldest first as the
// journal lists them; an unreadable or limited system journal is an error,
// never "no sleeps" (the automations dry run replays resume triggers).
func (a *SleepAPI) Sleeps(ctx context.Context, since time.Time) ([]Sleep, error) {
	if a.cfg.CanReadSystem != nil && !a.cfg.CanReadSystem() {
		return nil, errors.New("power: the system journal is not readable")
	}
	res, err := a.cfg.Run(ctx, "journalctl-sleep", safeexec.SleepArgs(since.Unix())...)
	if err != nil && res.ExitCode != 1 {
		return nil, fmt.Errorf("power: sleeps: %w", err)
	}
	return parseSleeps(res.Stdout), nil
}
