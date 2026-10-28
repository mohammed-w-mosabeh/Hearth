package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// API serves the Scheduler.
type API struct {
	Stores map[string]*Store // by scope; ScopeSystem may be missing
	// SystemProblem says why root jobs cannot be managed (no broker, not
	// a member of the hearth group), or "".
	SystemProblem func(ctx context.Context) string
	Run           Runner
	Audit         func(ctx context.Context, e store.AuditEntry) error
	UID           int
	Log           *slog.Logger
	// CanReadSystemJournal reports whether root jobs' history is readable.
	CanReadSystemJournal func() bool
}

func (a *API) store(scope string) (*Store, bool) {
	s, ok := a.Stores[scope]
	return s, ok
}

// HandleList serves GET /api/v1/jobs.
func (a *API) HandleList(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Jobs   []Listed `json:"jobs"`
		System string   `json:"system_problem,omitempty"`
	}{Jobs: []Listed{}}
	for _, scope := range []string{ScopeUser, ScopeSystem} {
		s, ok := a.store(scope)
		if !ok {
			continue
		}
		l, err := s.List(r.Context())
		if err != nil {
			a.Log.Warn("scheduler: list", "scope", scope, "err", err)
			continue
		}
		out.Jobs = append(out.Jobs, l...)
	}
	if a.SystemProblem != nil {
		out.System = a.SystemProblem(r.Context())
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleGet serves GET /api/v1/jobs/{scope}/{id}: the job with its script.
func (a *API) HandleGet(w http.ResponseWriter, r *http.Request) {
	s, id, ok := a.target(w, r)
	if !ok {
		return
	}
	j, err := s.Get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// HandleSave serves POST /api/v1/jobs/{scope}: create (no id) or update.
func (a *API) HandleSave(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	s, ok := a.store(scope)
	if !ok {
		http.Error(w, "scope must be user or system", http.StatusBadRequest)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var j Job
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		http.Error(w, "body must be a job", http.StatusBadRequest)
		return
	}
	if err := j.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if c := CheckSchedule(r.Context(), a.Run, j.Schedule); !c.Valid {
		http.Error(w, "schedule: "+c.Error, http.StatusBadRequest)
		return
	}
	isNew := j.ID == ""
	saved, err := s.Save(r.Context(), j)
	target := scope + "/" + saved.ID
	if isNew && err != nil {
		target = scope + "/new"
	}
	a.audit(r.Context(), "job/save", target, map[string]string{"name": j.Name, "schedule": j.Schedule.Kind + ":" + j.Schedule.Value}, err)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// HandleCheck serves GET /api/v1/jobs/check?kind=&value=.
func (a *API) HandleCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, CheckSchedule(r.Context(), a.Run, Schedule{Kind: q.Get("kind"), Value: q.Get("value")}))
}

// HandleHistory serves GET /api/v1/jobs/{scope}/{id}/history.
func (a *API) HandleHistory(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	if _, _, ok := a.target(w, r); !ok {
		return
	}
	runs, err := History(r.Context(), a.Run, scope, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	limited := scope == ScopeSystem && a.CanReadSystemJournal != nil && !a.CanReadSystemJournal()
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "limited": limited})
}

func (a *API) target(w http.ResponseWriter, r *http.Request) (*Store, string, bool) {
	s, ok := a.store(r.PathValue("scope"))
	id := r.PathValue("id")
	if !ok || !ValidID(id) {
		http.Error(w, "bad job", http.StatusBadRequest)
		return nil, "", false
	}
	return s, id, true
}

func (a *API) fail(w http.ResponseWriter, err error) {
	var p Problem
	switch {
	case errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist):
		http.Error(w, "no such job", http.StatusNotFound)
	case errors.As(err, &p):
		http.Error(w, p.Msg, http.StatusUnprocessableEntity)
	default:
		a.Log.Warn("scheduler", "err", err)
		http.Error(w, "the job could not be changed: "+err.Error(), http.StatusBadGateway)
	}
}

func (a *API) audit(ctx context.Context, action, target string, params map[string]string, err error) {
	if a.Audit == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	if aerr := a.Audit(context.WithoutCancel(ctx), store.AuditEntry{Origin: store.OriginLocal, UID: a.UID,
		Action: action, Target: target, Params: params, Result: result}); aerr != nil {
		a.Log.Warn("scheduler: audit", "err", aerr)
	}
}

// Target parses an action target "user/<id>" or "system/<id>".
func Target(t string) (scope, id string, err error) {
	scope, id, ok := strings.Cut(t, "/")
	if !ok || (scope != ScopeUser && scope != ScopeSystem) || !ValidID(id) {
		return "", "", errors.New(`target must be "user/<id>" or "system/<id>"`)
	}
	return scope, id, nil
}

// Register adds job/run, job/pause, job/resume and job/delete.
func (a *API) Register(reg *actions.Registry) {
	ops := []struct {
		name string
		risk actions.Risk
		do   func(ctx context.Context, s *Store, id string) error
	}{
		{"run", actions.Safe, func(ctx context.Context, s *Store, id string) error { return s.W.Run(ctx, id) }},
		{"pause", actions.Reversible, func(ctx context.Context, s *Store, id string) error { return s.W.SetPaused(ctx, id, true) }},
		{"resume", actions.Safe, func(ctx context.Context, s *Store, id string) error { return s.W.SetPaused(ctx, id, false) }},
		{"delete", actions.Destructive, func(ctx context.Context, s *Store, id string) error { return s.W.Delete(ctx, id) }},
	}
	for _, o := range ops {
		reg.Register(actions.Action{Kind: "job", Name: o.name, Risk: o.risk,
			Validate: func(r actions.Request) error {
				if len(r.Params) != 0 {
					return errors.New("params must be empty")
				}
				_, _, err := Target(r.Target)
				return err
			},
			Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
				scope, id, _ := Target(r.Target)
				s, ok := a.store(scope)
				if !ok {
					return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "root jobs are not available")
				}
				j, found := s.read(id)
				if !found {
					return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no such job")
				}
				return actions.Plan{Summary: o.name + " " + j.Name, Items: []actions.Item{{ID: r.Target, Name: j.Name, Detail: scope}},
					Blocked: []actions.Blocked{}}, nil
			},
			Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
				scope, id, _ := Target(r.Target)
				s, ok := a.store(scope)
				if !ok {
					return actions.Result{}, actions.Errorf(http.StatusServiceUnavailable, "root jobs are not available")
				}
				res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
				if err := o.do(ctx, s, id); err != nil {
					res.Failed = append(res.Failed, actions.Blocked{Item: p.Items[0], Reason: reasonOf(err)})
					res.Failed[0].Detail = err.Error()
					return res, nil
				}
				res.Done = 1
				return res, nil
			}})
	}
}

// reasonOf is the UI reason code of a failure.
func reasonOf(err error) string {
	if errors.Is(err, ErrUnauthorized) {
		return "unauthorized"
	}
	return "error"
}

// ErrUnauthorized is returned when polkit refused a root job change.
var ErrUnauthorized = errors.New("not authorized")

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
