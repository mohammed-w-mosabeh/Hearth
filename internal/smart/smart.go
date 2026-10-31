// Package smart is hearthd's side of disk health (SPEC Disks, SECURITY
// S16f): it asks hearth-broker for each disk's SMART report, keeps the last
// answers for the Disks page, feeds the "SMART failing" alert and starts
// self-tests.
package smart

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const (
	// Metric is the alert catalog's id.
	Metric = "disk.smart_failing"
	// pollEvery paces the background check. SMART changes slowly, and a
	// disk in standby is not woken for it.
	pollEvery = 30 * time.Minute
	// firstPoll lets hearthd finish starting.
	firstPoll = 30 * time.Second
	// freshFor is how long the page is served the last answers.
	freshFor = 20 * time.Second
	// callTimeout bounds one disk's report.
	callTimeout = 35 * time.Second
)

// What the page can say about the whole feature.
const (
	StatusOK          = "ok"
	StatusUnavailable = "unavailable"   // hearth-broker is not installed or running
	StatusNotMember   = "not-member"    // the user is not in the hearth group
	StatusNoSmartctl  = "not-installed" // smartmontools is not installed
)

// Broker is the subset of broker.Client used here.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Config configures a Monitor.
type Config struct {
	Broker  Broker
	SysRoot string // default /sys
	// Observe receives each disk's state: 1 failing, 0 not. It is called
	// only with an answer for every disk that reports SMART, so a disk
	// that sleeps or a broker that is away never resolves an incident.
	Observe func(ctx context.Context, now time.Time, values map[string]float64)
	Log     *slog.Logger
}

// Disk is one disk on the page.
type Disk struct {
	broker.SmartReport
	Error string `json:"error,omitempty"` // the last call failed; the figures are the last known
	At    int64  `json:"at,omitempty"`    // unix ms of the figures
}

// Health is GET /api/v1/disks/health.
type Health struct {
	Status string `json:"status"`
	Disks  []Disk `json:"disks"`
}

// Monitor keeps the last reports.
type Monitor struct {
	cfg Config

	refresh sync.Mutex // one round at a time
	mu      sync.Mutex
	known   map[string]Disk
	status  string
	at      time.Time
}

// New returns a Monitor.
func New(cfg Config) *Monitor {
	if cfg.SysRoot == "" {
		cfg.SysRoot = broker.SysRoot
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Monitor{cfg: cfg, known: map[string]Disk{}, status: StatusUnavailable}
}

// Names lists this machine's whole disks that can have SMART.
func (m *Monitor) Names() []string {
	entries, err := os.ReadDir(filepath.Join(m.cfg.SysRoot, "block"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if safeexec.DiskName(e.Name()) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.cfg.SysRoot, "block", e.Name(), "device")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// statusOf says what a call's error means for the whole feature, "" when
// it is about one disk.
func statusOf(err error) string {
	switch {
	case errors.Is(err, broker.ErrNoSmartctl):
		return StatusNoSmartctl
	case errors.Is(err, broker.ErrNotMember):
		return StatusNotMember
	case errors.Is(err, broker.ErrUnavailable):
		return StatusUnavailable
	}
	return ""
}

// Refresh asks the broker about every disk, unless the last round is
// recent enough (force skips that).
func (m *Monitor) Refresh(ctx context.Context, force bool) {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	m.mu.Lock()
	recent := time.Since(m.at) < freshFor
	m.mu.Unlock()
	if recent && !force {
		return
	}
	now := time.Now()
	names := m.Names()
	status := StatusOK
	found := make(map[string]Disk, len(names))
	for _, name := range names {
		m.mu.Lock()
		d := m.known[name]
		m.mu.Unlock()
		d.Device, d.Error = name, ""
		var r broker.SmartReport
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		err := m.cfg.Broker.Call(cctx, "disk.smart", broker.SmartParams{Device: name}, &r)
		cancel()
		switch {
		case err != nil:
			if s := statusOf(err); s != "" {
				status = s
			} else {
				d.Error = err.Error()
				m.cfg.Log.Warn("smart: read", "disk", name, "err", err)
			}
		case r.Asleep:
			d.Asleep = true // the last figures stand
		default:
			d.SmartReport, d.At = r, now.UnixMilli()
		}
		found[name] = d
		if status != StatusOK {
			break // the same for every disk
		}
	}
	m.mu.Lock()
	m.status, m.at = status, now
	if status == StatusOK {
		m.known = found
	}
	m.mu.Unlock()
	if status != StatusOK {
		return
	}
	if v, ok := values(found); ok && m.cfg.Observe != nil {
		m.cfg.Observe(ctx, now, v)
	}
}

// values is the alert's input. A disk with no figures yet (asleep since
// hearthd started, or failing to answer) could be one with an open
// incident, so ok is false rather than leaving it out.
func values(disks map[string]Disk) (map[string]float64, bool) {
	out := map[string]float64{}
	for name, d := range disks {
		if d.At == 0 {
			return nil, false
		}
		if !d.Supported {
			continue
		}
		out[name] = 0
		if d.Failing {
			out[name] = 1
		}
	}
	return out, true
}

// Run polls until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	wait := firstPoll
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = pollEvery
		m.Refresh(ctx, true)
	}
}

// Snapshot is the page's data.
func (m *Monitor) Snapshot() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := Health{Status: m.status, Disks: make([]Disk, 0, len(m.known))}
	for _, d := range m.known {
		h.Disks = append(h.Disks, d)
	}
	sort.Slice(h.Disks, func(i, j int) bool { return h.Disks[i].Device < h.Disks[j].Device })
	return h
}

// Handle serves GET /api/v1/disks/health.
func (m *Monitor) Handle(w http.ResponseWriter, r *http.Request) {
	// Not r.Context(): a closed tab must not read as a broker that is away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	m.Refresh(ctx, false)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(m.Snapshot())
}

// Register adds disk/selftest. It is Safe in the registry's terms: it
// changes no data, and the broker asks polkit before it starts.
func (m *Monitor) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "disk", Name: "selftest", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if err := safeexec.DiskName(r.Target); err != nil {
				return errors.New("target must be a whole disk, like sda or nvme0n1")
			}
			if k := r.Params["kind"]; len(r.Params) != 1 || (k != "short" && k != "long") {
				return errors.New(`params must be {"kind": "short" or "long"}`)
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: r.Params["kind"] + " self-test of " + r.Target, Blocked: []actions.Blocked{},
				Items: []actions.Item{{ID: r.Target, Name: r.Target}}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
			it := p.Items[0]
			err := m.cfg.Broker.Call(ctx, "disk.selftest", broker.SelfTestParams{Device: r.Target, Kind: r.Params["kind"]}, nil)
			var be *broker.Error
			switch {
			case err == nil:
				res.Done = 1
				m.Refresh(ctx, true)
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
				return res, actions.Errorf(http.StatusBadGateway, "broker: %v", err)
			}
			return res, nil
		}})
}
