package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Scopes of a job: run as the user, or as root.
const (
	ScopeUser   = "user"
	ScopeSystem = "system"
)

// Bus is a systemd manager connection (*dbusx.Conn).
type Bus interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	GetAll(ctx context.Context, dest string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)
	Close() error
}

const (
	sdDest    = "org.freedesktop.systemd1"
	sdPath    = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManager = "org.freedesktop.systemd1.Manager"
)

// Writer changes one scope's jobs: files on disk and the manager's state.
type Writer interface {
	// Save writes the job's script and units and (re)starts its timer
	// unless paused.
	Save(ctx context.Context, j Job, paused bool) error
	Delete(ctx context.Context, id string) error
	Run(ctx context.Context, id string) error
	SetPaused(ctx context.Context, id string, paused bool) error
	// Script reads a job's script.
	Script(ctx context.Context, id string) (string, error)
}

// Listed is a job as the Scheduler lists it.
type Listed struct {
	Job
	Scope   string `json:"scope"`
	Paused  bool   `json:"paused"`         // its timer is not running
	Running bool   `json:"running"`        // a run is in progress
	Failed  bool   `json:"failed"`         // the last run failed
	Next    int64  `json:"next,omitempty"` // Unix ms of the next run
	Last    int64  `json:"last,omitempty"` // Unix ms of the last trigger
	Result  string `json:"result,omitempty"`
	Exit    *int   `json:"exit,omitempty"`
}

// Store reads one scope's jobs from its unit directory and its manager.
type Store struct {
	Scope   string
	UnitDir string // ~/.config/systemd/user or /etc/systemd/system
	Bus     func(context.Context) (Bus, error)
	W       Writer
}

// ErrUnauthorized is returned when polkit refused a root job change.
var ErrUnauthorized = errors.New("not authorized")

// ErrNotFound is returned for an unknown job.
var ErrNotFound = errors.New("no such job")

// maxJobs bounds one listing.
const maxJobs = 500

// Read returns a job's definition from its unit files.
func (s *Store) Read(id string) (Job, bool) {
	base := filepath.Join(s.UnitDir, UnitBase(id))
	svc, err1 := readSmall(base + ".service")
	tm, err2 := readSmall(base + ".timer")
	if err1 != nil || err2 != nil {
		return Job{}, false
	}
	return FromUnits(id, svc, tm)
}

func readSmall(p string) (string, error) {
	f, err := os.Open(p) // #nosec G304 G703 -- a hearth-job unit in the scope's unit directory (id: 8 hex digits)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 16<<10)
	n, err := f.Read(b)
	if err != nil {
		return "", err
	}
	return string(b[:n]), nil
}

// IDs lists the job ids found in the unit directory.
func (s *Store) IDs() []string {
	entries, _ := os.ReadDir(s.UnitDir)
	var ids []string
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), Prefix)
		if !ok {
			continue
		}
		if id, ok := strings.CutSuffix(rest, ".timer"); ok && ValidID(id) && len(ids) < maxJobs {
			ids = append(ids, id)
		}
	}
	return ids
}

// List returns the scope's jobs with their state.
func (s *Store) List(ctx context.Context) ([]Listed, error) {
	out := []Listed{}
	ids := s.IDs()
	if len(ids) == 0 {
		return out, nil
	}
	bus, err := s.Bus(ctx)
	if err != nil {
		bus = nil
	} else {
		defer func() { _ = bus.Close() }()
	}
	for _, id := range ids {
		j, ok := s.Read(id)
		if !ok {
			continue
		}
		l := Listed{Job: j, Scope: s.Scope, Paused: true}
		if bus != nil {
			state(ctx, bus, id, &l)
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Get returns one job with its script.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	j, ok := s.Read(id)
	if !ok {
		return Job{}, ErrNotFound
	}
	script, err := s.W.Script(ctx, id)
	if err != nil {
		return Job{}, err
	}
	j.Script = script
	return j, nil
}

// Save creates (j.ID == "") or updates a job. A paused job stays paused.
func (s *Store) Save(ctx context.Context, j Job) (Job, error) {
	if err := j.Validate(); err != nil {
		return Job{}, err
	}
	paused := false
	if j.ID == "" {
		for range 5 {
			b := make([]byte, 4)
			_, _ = rand.Read(b)
			if id := hex.EncodeToString(b); !s.exists(id) {
				j.ID = id
				break
			}
		}
		if j.ID == "" {
			return Job{}, errors.New("no free job id")
		}
	} else {
		if !s.exists(j.ID) {
			return Job{}, ErrNotFound
		}
		paused = s.paused(ctx, j.ID)
	}
	if err := s.W.Save(ctx, j, paused); err != nil {
		return Job{}, err
	}
	return j, nil
}

func (s *Store) exists(id string) bool {
	_, err := os.Stat(filepath.Join(s.UnitDir, UnitBase(id)+".timer"))
	return err == nil
}

func (s *Store) paused(ctx context.Context, id string) bool {
	bus, err := s.Bus(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	var l Listed
	l.Paused = true
	state(ctx, bus, id, &l)
	return l.Paused
}

// unitPath returns the D-Bus object of a unit, loading it from disk if
// systemd has not yet (LoadUnit changes nothing else).
func unitPath(ctx context.Context, bus Bus, name string) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".GetUnit", []any{&p}, name); err == nil {
		return p, nil
	}
	err := bus.Call(ctx, sdDest, sdPath, sdManager+".LoadUnit", []any{&p}, name)
	return p, err
}

// state fills a job's timer and last-run state from systemd.
func state(ctx context.Context, bus Bus, id string, l *Listed) {
	base := UnitBase(id)
	if p, err := unitPath(ctx, bus, base+".timer"); err == nil {
		if u, err := bus.GetAll(ctx, sdDest, p, "org.freedesktop.systemd1.Unit"); err == nil {
			a, _ := u["ActiveState"].Value().(string)
			l.Paused = a != "active"
		}
		if t, err := bus.GetAll(ctx, sdDest, p, "org.freedesktop.systemd1.Timer"); err == nil {
			if v, _ := t["NextElapseUSecRealtime"].Value().(uint64); v > 0 && !l.Paused {
				l.Next = int64(v / 1000) // #nosec G115 -- microseconds since 1970
			}
			if v, _ := t["LastTriggerUSec"].Value().(uint64); v > 0 {
				l.Last = int64(v / 1000) // #nosec G115 -- microseconds since 1970
			}
		}
	}
	if p, err := unitPath(ctx, bus, base+".service"); err == nil {
		if u, err := bus.GetAll(ctx, sdDest, p, "org.freedesktop.systemd1.Unit"); err == nil {
			a, _ := u["ActiveState"].Value().(string)
			l.Running = a == "activating"
			l.Failed = a == "failed"
		}
		if sv, err := bus.GetAll(ctx, sdDest, p, "org.freedesktop.systemd1.Service"); err == nil && l.Last > 0 {
			l.Result, _ = sv["Result"].Value().(string)
			if code, ok := sv["ExecMainStatus"].Value().(int32); ok && !l.Running {
				n := int(code)
				l.Exit = &n
			}
		}
	}
}

// Problem is a user-facing error from a store.
type Problem struct{ Msg string }

func (p Problem) Error() string { return p.Msg }

// SplitFailures takes failed units ("<scope>/<unit>" -> 1, as the failed
// unit poller reports them) and returns the others, and the failed jobs
// that notify on failure, by name. Jobs are left out of the units either
// way: a job's failure is the Scheduler's to report.
func SplitFailures(failed map[string]float64, stores map[string]*Store) (units, jobs map[string]float64) {
	units, jobs = map[string]float64{}, map[string]float64{}
	for k, v := range failed {
		scope, unit, _ := strings.Cut(k, "/")
		id, ok := strings.CutSuffix(strings.TrimPrefix(unit, Prefix), ".service")
		if !strings.HasPrefix(unit, Prefix) || !ok || !ValidID(id) {
			units[k] = v
			continue
		}
		s, ok := stores[scope]
		if !ok {
			continue
		}
		if j, ok := s.Read(id); ok && j.Notify {
			jobs[j.Name] = v
		}
	}
	return units, jobs
}
