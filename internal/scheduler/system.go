package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Root jobs (SECURITY S13) are written by hearth-broker: their script is
// /var/lib/hearth/jobs/<id>/run, owned by root, mode 0755, never in a
// path the user can write; their units are in /etc/systemd/system. Every
// change, and reading a script, is authorized by polkit
// (org.hearth.jobs.manage) after the params are validated.

// ActionJobs is the polkit action for root jobs.
const ActionJobs = "org.hearth.jobs.manage"

// SystemUnitDir is where root jobs' units live.
const SystemUnitDir = "/etc/systemd/system"

// SystemJobsDir is where root jobs' scripts live.
const SystemJobsDir = "/var/lib/hearth/jobs"

// SaveParams are sched.save's params.
type SaveParams struct {
	Job    Job  `json:"job"`
	Create bool `json:"create"` // a new id: refuse if it exists
}

// IDParams are the params of the other sched.* methods.
type IDParams struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused,omitempty"` // sched.pause
}

// BrokerConfig is where the broker's methods write (tests point it at a
// temporary directory).
type BrokerConfig struct {
	UnitDir, JobsDir string
	Bus              func(context.Context) (Bus, error) // the system bus
	Run              Runner                             // systemd-analyze, to check schedules
}

// BrokerMethods are hearth-broker's sched.* methods.
func BrokerMethods(c BrokerConfig) []broker.Method {
	w := &UserWriter{UnitDir: c.UnitDir, JobsDir: c.JobsDir, Bus: c.Bus, ScriptMode: 0o755, DirMode: 0o755, UnitMode: 0o644}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(c.UnitDir, UnitBase(id)+".timer"))
		return err == nil
	}
	saveParams := func(ctx context.Context, p json.RawMessage) (SaveParams, error) {
		var v SaveParams
		if err := decode(p, &v); err != nil {
			return v, err
		}
		if !ValidID(v.Job.ID) {
			return v, broker.Errorf(broker.CodeBadParams, "bad job id")
		}
		if err := v.Job.Validate(); err != nil {
			return v, broker.Errorf(broker.CodeBadParams, "%v", err)
		}
		if c.Run != nil {
			if chk := CheckSchedule(ctx, c.Run, v.Job.Schedule); !chk.Valid {
				return v, broker.Errorf(broker.CodeBadParams, "schedule: %s", chk.Error)
			}
		}
		switch {
		case v.Create && exists(v.Job.ID):
			return v, broker.Errorf(broker.CodeBadParams, "job %s exists", v.Job.ID)
		case !v.Create && !exists(v.Job.ID):
			return v, broker.Errorf(broker.CodeNotFound, "no such job")
		}
		return v, nil
	}
	idParams := func(p json.RawMessage) (IDParams, error) {
		var v IDParams
		if err := decode(p, &v); err != nil {
			return v, err
		}
		if !ValidID(v.ID) {
			return v, broker.Errorf(broker.CodeBadParams, "bad job id")
		}
		if !exists(v.ID) {
			return v, broker.Errorf(broker.CodeNotFound, "no such job")
		}
		return v, nil
	}
	failed := func(err error) error {
		if err == nil {
			return nil
		}
		return broker.Errorf(broker.CodeFailed, "%s", err.Error())
	}
	idMethod := func(name string, timeout time.Duration, do func(ctx context.Context, v IDParams) (any, error)) broker.Method {
		return broker.Method{Name: name, Privileged: true, Action: ActionJobs, Timeout: timeout,
			Check: func(_ context.Context, p json.RawMessage) error { _, err := idParams(p); return err },
			Handle: func(ctx context.Context, _ broker.Caller, p json.RawMessage) (any, error) {
				v, err := idParams(p)
				if err != nil {
					return nil, err
				}
				return do(ctx, v)
			}}
	}
	return []broker.Method{
		{Name: "sched.save", Privileged: true, Action: ActionJobs, Timeout: 2 * time.Minute,
			Check: func(ctx context.Context, p json.RawMessage) error { _, err := saveParams(ctx, p); return err },
			Handle: func(ctx context.Context, _ broker.Caller, p json.RawMessage) (any, error) {
				v, err := saveParams(ctx, p)
				if err != nil {
					return nil, err
				}
				paused := false
				if !v.Create {
					paused = pausedOn(ctx, c.Bus, v.Job.ID)
				}
				return map[string]string{"id": v.Job.ID}, failed(w.Save(ctx, v.Job, paused))
			}},
		idMethod("sched.delete", 2*time.Minute, func(ctx context.Context, v IDParams) (any, error) {
			return map[string]bool{"deleted": true}, failed(w.Delete(ctx, v.ID))
		}),
		idMethod("sched.run", time.Minute, func(ctx context.Context, v IDParams) (any, error) {
			return map[string]bool{"started": true}, failed(w.Run(ctx, v.ID))
		}),
		idMethod("sched.pause", time.Minute, func(ctx context.Context, v IDParams) (any, error) {
			return map[string]bool{"paused": v.Paused}, failed(w.SetPaused(ctx, v.ID, v.Paused))
		}),
		idMethod("sched.script", time.Minute, func(ctx context.Context, v IDParams) (any, error) {
			s, err := w.Script(ctx, v.ID)
			return map[string]string{"script": s}, failed(err)
		}),
	}
}

func pausedOn(ctx context.Context, dial func(context.Context) (Bus, error), id string) bool {
	bus, err := dial(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	l := Listed{Paused: true}
	state(ctx, bus, id, &l)
	return l.Paused
}

// decode reads params strictly.
func decode(p json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return broker.Errorf(broker.CodeBadParams, "bad params")
	}
	return nil
}

// Caller is the hearthd side of the broker (*broker.Client).
type Caller interface {
	Call(ctx context.Context, method string, params, out any) error
}

// SystemWriter manages root jobs through hearth-broker.
type SystemWriter struct {
	B Caller
	// Exists reports whether a job's units are there (hearthd reads
	// /etc/systemd/system itself).
	Exists func(id string) bool
}

func (s *SystemWriter) call(ctx context.Context, method string, params, out any) error {
	err := s.B.Call(ctx, method, params, out)
	var be *broker.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, broker.ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, broker.ErrNotMember):
		return Problem{Msg: "you are not in the hearth group, so Hearth cannot manage root jobs"}
	case errors.Is(err, broker.ErrUnavailable):
		return Problem{Msg: "hearth-broker is not available, so Hearth cannot manage root jobs"}
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		return ErrNotFound
	case errors.As(err, &be):
		return Problem{Msg: be.Message}
	}
	return fmt.Errorf("scheduler: %w", err)
}

// Save implements Writer (the broker decides whether it stays paused).
func (s *SystemWriter) Save(ctx context.Context, j Job, _ bool) error {
	return s.call(ctx, "sched.save", SaveParams{Job: j, Create: !s.Exists(j.ID)}, nil)
}

// Delete implements Writer.
func (s *SystemWriter) Delete(ctx context.Context, id string) error {
	return s.call(ctx, "sched.delete", IDParams{ID: id}, nil)
}

// Run implements Writer.
func (s *SystemWriter) Run(ctx context.Context, id string) error {
	return s.call(ctx, "sched.run", IDParams{ID: id}, nil)
}

// SetPaused implements Writer.
func (s *SystemWriter) SetPaused(ctx context.Context, id string, paused bool) error {
	return s.call(ctx, "sched.pause", IDParams{ID: id, Paused: paused}, nil)
}

// Script implements Writer.
func (s *SystemWriter) Script(ctx context.Context, id string) (string, error) {
	var out struct {
		Script string `json:"script"`
	}
	err := s.call(ctx, "sched.script", IDParams{ID: id}, &out)
	return out.Script, err
}
