// Package lynis is the Lynis section of the Security page (ROADMAP 7.1): it
// asks the broker to run an audit or set a schedule, keeps each run's
// hardening index, and raises an alert when the index drops. Lynis only ever
// runs on request or on the schedule the user set (SECURITY S16h); reading
// the last report starts nothing.
package lynis

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Metric is the alert catalog's id.
const Metric = "lynis.index_drop"

const (
	watchEvery = time.Hour // a schedule's run shows up within this, or when the page opens
	maxHistory = 60
)

// Broker is the part of broker.Client the package needs.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Store keeps the runs (store.DB in production).
type Store interface {
	AddLynisRun(ctx context.Context, r store.LynisRun) error
	LynisRuns(ctx context.Context, limit int) ([]store.LynisRun, error)
}

// Auditor appends to the audit log (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Config configures a Manager.
type Config struct {
	Broker  Broker
	Store   Store
	Audit   Auditor
	Observe func(ctx context.Context, now time.Time, v map[string]float64)
	Now     func() time.Time
	Log     *slog.Logger
	Binary  string // default broker.LynisBinary
}

// Manager runs the routes and the watch.
type Manager struct {
	cfg     Config
	running atomic.Bool
	mu      sync.Mutex // ingest
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.Binary == "" {
		cfg.Binary = broker.LynisBinary
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg}
}

// View is GET /api/v1/security/lynis's answer.
type View struct {
	broker.LynisState
	Running bool             `json:"running"`
	Error   string           `json:"error,omitempty"`
	History []store.LynisRun `json:"history"` // newest first
}

// ingest stores a report it has not seen and tells the alert engine whether
// the index fell from the run before. It observes either way, so last
// time's incident ends: one notification.
func (m *Manager) ingest(ctx context.Context, r *broker.LynisReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	news := map[string]float64{}
	if r != nil {
		runs, err := m.cfg.Store.LynisRuns(ctx, 1)
		if err != nil {
			return err
		}
		if len(runs) == 0 || r.Time > runs[0].Time {
			if err := m.cfg.Store.AddLynisRun(ctx, store.LynisRun{Time: r.Time, Index: r.Index, Warnings: len(r.Warnings), Suggestions: len(r.Suggestions)}); err != nil {
				return err
			}
			if len(runs) == 1 && r.Index < runs[0].Index {
				news["from "+strconv.Itoa(runs[0].Index)+" to "+strconv.Itoa(r.Index)] = 1
			}
		}
	}
	m.cfg.Observe(ctx, m.cfg.Now(), news)
	return nil
}

func (m *Manager) installed() bool {
	_, err := os.Stat(m.cfg.Binary)
	return err == nil
}

// watch picks up what a schedule's run left.
func (m *Manager) watch(ctx context.Context) {
	if !m.installed() || m.running.Load() {
		return
	}
	var s broker.LynisState
	if err := m.cfg.Broker.Call(ctx, "lynis.report", nil, &s); err != nil {
		// A broker that is gone is no news: the next hour asks again.
		m.cfg.Log.Debug("lynis: report", "err", err)
		return
	}
	if err := m.ingest(ctx, s.Report); err != nil && ctx.Err() == nil {
		m.cfg.Log.Warn("lynis: ingest", "err", err)
	}
}

// Run watches until ctx ends. The first look is an hour in: starting up
// must not wake the broker, which exits when idle.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.watch(ctx)
		}
	}
}

func (m *Manager) view(ctx context.Context) View {
	v := View{Running: m.running.Load(), History: []store.LynisRun{}}
	v.Installed, v.Schedule = m.installed(), "off"
	if v.Installed {
		var s broker.LynisState
		if err := m.cfg.Broker.Call(ctx, "lynis.report", nil, &s); err != nil {
			v.Error = message(err)
		} else {
			v.LynisState = s
			if err := m.ingest(ctx, s.Report); err != nil {
				m.cfg.Log.Warn("lynis: ingest", "err", err)
			}
		}
	}
	h, err := m.cfg.Store.LynisRuns(ctx, maxHistory)
	if err != nil {
		m.cfg.Log.Warn("lynis: history", "err", err)
	} else {
		v.History = h
	}
	return v
}

// message is what the page may show of a broker error.
func message(err error) string {
	var be *broker.Error
	if errors.As(err, &be) {
		return be.Message
	}
	return "the broker is not available"
}

func status(err error) int {
	var be *broker.Error
	if errors.As(err, &be) {
		switch be.Code {
		case broker.CodeUnauthorized, broker.CodeNotMember:
			return http.StatusForbidden
		case broker.CodeBadParams:
			return http.StatusBadRequest
		}
	}
	return http.StatusBadGateway
}

func (m *Manager) audit(ctx context.Context, action, target string, err error, ok string) error {
	result := "ok: " + ok
	if err != nil {
		result = "error: " + message(err)
	}
	return m.cfg.Audit.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action, Target: target, Result: result})
}

// handleRun runs an audit. A closed page must not leave the run unrecorded,
// so the request's cancellation does not reach it.
func (m *Manager) handleRun(w http.ResponseWriter, r *http.Request) {
	if !m.running.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Lynis is already running"})
		return
	}
	defer m.running.Store(false)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), broker.LynisTimeout+time.Minute)
	defer cancel()
	var s broker.LynisState
	err := m.cfg.Broker.Call(ctx, "lynis.run", nil, &s)
	note := ""
	if err == nil && s.Report != nil {
		note = "hardening index " + strconv.Itoa(s.Report.Index)
	}
	if aerr := m.audit(ctx, "security.lynis.run", "lynis", err, note); aerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "audit log unavailable"})
		return
	}
	if err != nil {
		writeJSON(w, status(err), map[string]string{"error": message(err)})
		return
	}
	if err := m.ingest(ctx, s.Report); err != nil {
		m.cfg.Log.Warn("lynis: ingest", "err", err)
	}
	m.running.Store(false)
	writeJSON(w, http.StatusOK, m.view(ctx))
}

// handleSchedule sets the schedule. The broker validates it, before polkit.
func (m *Manager) handleSchedule(w http.ResponseWriter, r *http.Request) {
	var p broker.LynisScheduleParams
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	err := m.cfg.Broker.Call(ctx, "lynis.schedule", p, nil)
	var be *broker.Error
	if errors.As(err, &be) && be.Code == broker.CodeBadParams {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": be.Message})
		return // nothing was asked of anyone, and the value is the caller's
	}
	if aerr := m.audit(ctx, "security.lynis.schedule", p.Every, err, p.Every); aerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "audit log unavailable"})
		return
	}
	if err != nil {
		writeJSON(w, status(err), map[string]string{"error": message(err)})
		return
	}
	writeJSON(w, http.StatusOK, m.view(ctx))
}

// Register adds the routes.
func (m *Manager) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/security/lynis", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.view(r.Context()))
	})
	mux.HandleFunc("POST /api/v1/security/lynis/run", m.handleRun)
	mux.HandleFunc("PUT /api/v1/security/lynis/schedule", m.handleSchedule)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
