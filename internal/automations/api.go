// Package automations is Hearth's rules engine (ROADMAP Phase 12,
// SECURITY S15d, docs/plan/automations.md): storage, the API, and from
// 12.3 the engine. Specs are validated by package spec wherever they come
// from; this package adds the checks that need the machine.
package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Store is what the API needs (store.DB).
type Store interface {
	Automations(ctx context.Context) ([]store.Automation, error)
	GetAutomation(ctx context.Context, id int64) (store.Automation, error)
	CreateAutomations(ctx context.Context, list []store.Automation) ([]int64, error)
	UpdateAutomation(ctx context.Context, a store.Automation) error
	SetAutomationEnabled(ctx context.Context, id int64, on bool) error
	DeleteAutomation(ctx context.Context, id int64) error
	AutomationRuns(ctx context.Context, automation int64, limit int) ([]store.AutomationRun, error)
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Checker checks what depends on the machine: every action is
// registered, every schedule parses. It runs at every save.
type Checker func(ctx context.Context, s spec.Spec) error

// API serves /api/v1/automations.
type API struct {
	Store Store
	Check Checker
	// Engine holds the kill switch; nil serves no pause routes.
	Engine interface {
		Paused(ctx context.Context) bool
		SetPaused(ctx context.Context, on bool, by string) error
	}
	// Changed is told after any change, so the engine reloads.
	Changed func()
	// Approvals lists the waiting approvals (the engine); ModeApprovals
	// those a mode switch left (modes.Approvals.Pending). Either may be nil.
	Approvals interface {
		Approvals(ctx context.Context) ([]ApprovalView, error)
	}
	ModeApprovals func(ctx context.Context) (any, error)
	// Tester runs Test now (the engine); nil serves no test route.
	Tester interface {
		Test(ctx context.Context, id int64, check func(context.Context, spec.Spec) error) error
	}
	// DryRun replays a spec over the last 7 days (the engine); nil serves
	// no dry-run route.
	DryRun func(ctx context.Context, id int64, s spec.Spec) (DryRun, error)
}

// View is an automation as the API shows it.
type View struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Origin  string     `json:"origin"`
	Spec    *spec.Spec `json:"spec,omitempty"`
	// Invalid is set when the stored spec no longer passes the validator
	// (it never runs then).
	Invalid string `json:"invalid,omitempty"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

// Body is what create and update take.
type Body struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Spec    json.RawMessage `json:"spec"`
}

const maxBody = 32 << 10

// Register adds the routes to mux.
func (a *API) Register(mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}) {
	mux.HandleFunc("GET /api/v1/automations", a.list)
	mux.HandleFunc("POST /api/v1/automations", a.create)
	mux.HandleFunc("PUT /api/v1/automations/{id}", a.update)
	mux.HandleFunc("PUT /api/v1/automations/{id}/enabled", a.enable)
	mux.HandleFunc("DELETE /api/v1/automations/{id}", a.remove)
	mux.HandleFunc("GET /api/v1/automations/{id}/runs", a.runs)
	mux.HandleFunc("POST /api/v1/automations/import", a.importFile)
	mux.HandleFunc("GET /api/v1/automations/export", a.export)
	mux.HandleFunc("GET /api/v1/automations/approvals", a.approvals)
	if a.Tester != nil {
		mux.HandleFunc("POST /api/v1/automations/{id}/test", a.test)
	}
	if a.DryRun != nil {
		mux.HandleFunc("POST /api/v1/automations/dry-run", a.dryRun)
	}
	if a.Engine != nil {
		mux.HandleFunc("GET /api/v1/automations/pause", a.getPause)
		mux.HandleFunc("PUT /api/v1/automations/pause", a.setPause)
	}
}

// approvals serves the approval screen's list: automation approvals and
// mode approvals together. Each is approved or dismissed through its own
// action (automation/approve, mode/approve), with a preview first.
func (a *API) approvals(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Automations []ApprovalView `json:"automations"`
		Modes       any            `json:"modes"`
	}{Automations: []ApprovalView{}, Modes: []any{}}
	if a.Approvals != nil {
		list, err := a.Approvals.Approvals(r.Context())
		if err != nil {
			httpError(w, http.StatusInternalServerError, "approvals could not be read")
			return
		}
		out.Automations = list
	}
	if a.ModeApprovals != nil {
		list, err := a.ModeApprovals(r.Context())
		if err != nil {
			httpError(w, http.StatusInternalServerError, "approvals could not be read")
			return
		}
		out.Modes = list
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getPause(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"paused": a.Engine.Paused(r.Context())})
}

// setPause is the kill switch: "Pause all automations".
func (a *API) setPause(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Paused *bool `json:"paused"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || b.Paused == nil {
		httpError(w, http.StatusBadRequest, "body must be {paused}")
		return
	}
	if err := a.Engine.SetPaused(r.Context(), *b.Paused, "you"); err != nil {
		httpError(w, http.StatusInternalServerError, "the switch could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": *b.Paused})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// view decodes a stored automation; a spec that no longer validates is
// shown as invalid.
func view(a store.Automation) View {
	v := View{ID: a.ID, Name: a.Name, Enabled: a.Enabled, Origin: a.Origin, Created: a.Created, Updated: a.Updated}
	s, err := spec.Parse(a.Spec)
	if err != nil {
		v.Invalid = err.Error()
		return v
	}
	v.Spec = &s
	return v
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Automations(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "automations could not be read")
		return
	}
	out := []View{}
	for _, x := range list {
		out = append(out, view(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": out})
}

func (a *API) audit(r *http.Request, action string, id int64, result string) {
	_ = a.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: action, Target: "automation:" + strconv.FormatInt(id, 10), Result: result})
	if a.Changed != nil {
		a.Changed()
	}
}

// decode reads and fully checks a body: shape, the validator, then the
// machine.
func (a *API) decode(r *http.Request) (store.Automation, error) {
	var b Body
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		return store.Automation{}, errors.New("body too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return store.Automation{}, errors.New("body must be {name, enabled, spec}")
	}
	if err := spec.CheckName(b.Name); err != nil {
		return store.Automation{}, err
	}
	s, err := spec.Parse(b.Spec)
	if err != nil {
		return store.Automation{}, err
	}
	if err := userMade(s); err != nil {
		return store.Automation{}, err
	}
	if a.Check != nil {
		if err := a.Check(r.Context(), s); err != nil {
			return store.Automation{}, err
		}
	}
	canon, err := json.Marshal(s)
	if err != nil {
		return store.Automation{}, err
	}
	return store.Automation{Name: b.Name, Enabled: b.Enabled, Spec: canon}, nil
}

func (a *API) id(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "bad automation id")
		return 0, false
	}
	return id, true
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	x, err := a.decode(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	x.Origin = "ui"
	ids, err := a.Store.CreateAutomations(r.Context(), []store.Automation{x})
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the automation could not be saved")
		return
	}
	a.audit(r, "automation.create", ids[0], "ok")
	got, _ := a.Store.GetAutomation(r.Context(), ids[0])
	writeJSON(w, http.StatusOK, view(got))
}

// owned reports whether id belongs to a mode file, which alone may change
// it (arming and disarming, S15b).
func (a *API) owned(r *http.Request, id int64) (store.Automation, int, error) {
	cur, err := a.Store.GetAutomation(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return cur, http.StatusNotFound, errors.New("no such automation")
	case err != nil:
		return cur, http.StatusInternalServerError, errors.New("the automation could not be read")
	case strings.HasPrefix(cur.Origin, "mode:"):
		return cur, http.StatusConflict, errors.New("this automation belongs to a mode's trigger: disarm it on the Modes page")
	}
	return cur, 0, nil
}

func (a *API) update(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	if _, code, err := a.owned(r, id); err != nil {
		httpError(w, code, err.Error())
		return
	}
	x, err := a.decode(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	x.ID = id
	if err := a.Store.UpdateAutomation(r.Context(), x); err != nil {
		httpError(w, http.StatusInternalServerError, "the automation could not be saved")
		return
	}
	a.audit(r, "automation.update", id, "ok")
	got, _ := a.Store.GetAutomation(r.Context(), id)
	writeJSON(w, http.StatusOK, view(got))
}

func (a *API) enable(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || b.Enabled == nil {
		httpError(w, http.StatusBadRequest, "body must be {enabled}")
		return
	}
	cur, code, err := a.owned(r, id)
	if err != nil {
		httpError(w, code, err.Error())
		return
	}
	if *b.Enabled {
		// Turning on checks the spec again: the machine may have changed.
		s, err := spec.Parse(cur.Spec)
		if err == nil && a.Check != nil {
			err = a.Check(r.Context(), s)
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := a.Store.SetAutomationEnabled(r.Context(), id, *b.Enabled); err != nil {
		httpError(w, http.StatusInternalServerError, "the automation could not be saved")
		return
	}
	a.audit(r, map[bool]string{true: "automation.enable", false: "automation.disable"}[*b.Enabled], id, "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *b.Enabled})
}

func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	if _, code, err := a.owned(r, id); err != nil {
		httpError(w, code, err.Error())
		return
	}
	if err := a.Store.DeleteAutomation(r.Context(), id); err != nil {
		httpError(w, http.StatusInternalServerError, "the automation could not be deleted")
		return
	}
	a.audit(r, "automation.delete", id, "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *API) runs(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	runs, err := a.Store.AutomationRuns(r.Context(), id, 100)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "runs could not be read")
		return
	}
	if runs == nil {
		runs = []store.AutomationRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// importFile stores a validated import, all or nothing. Every imported
// automation starts disabled.
func (a *API) importFile(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, spec.MaxImport+1))
	if err != nil {
		httpError(w, http.StatusBadRequest, "the import could not be read")
		return
	}
	items, err := spec.ParseImport(raw)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	list := make([]store.Automation, 0, len(items))
	for i, it := range items {
		if err := userMade(it.Spec); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("automation %d: %v", i+1, err))
			return
		}
		if a.Check != nil {
			if err := a.Check(r.Context(), it.Spec); err != nil {
				httpError(w, http.StatusBadRequest, fmt.Sprintf("automation %d: %v", i+1, err))
				return
			}
		}
		canon, err := json.Marshal(it.Spec)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "the import could not be stored")
			return
		}
		list = append(list, store.Automation{Name: it.Name, Enabled: false, Spec: canon, Origin: "import"})
	}
	ids, err := a.Store.CreateAutomations(r.Context(), list)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the import could not be stored")
		return
	}
	for _, id := range ids {
		a.audit(r, "automation.import", id, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": ids})
}

func (a *API) export(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Automations(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "automations could not be read")
		return
	}
	items := []spec.Item{}
	for _, x := range list {
		if strings.HasPrefix(x.Origin, "mode:") {
			continue // a mode file's trigger travels with the file
		}
		if s, err := spec.Parse(x.Spec); err == nil {
			items = append(items, spec.Item{Name: x.Name, Enabled: x.Enabled, Spec: s})
		}
	}
	b, err := spec.Export(items)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the export could not be written")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="hearth-automations.json"`)
	_, _ = w.Write(b)
}

// ModeActivate is the action only a mode trigger's automation holds: one
// made by arming that trigger with a click, for that file (SECURITY S15b).
const ModeActivate = "mode/activate"

// userMade refuses what an automation made in the editor or imported may
// not hold.
func userMade(s spec.Spec) error {
	held := false
	s.Steps(func(st spec.Step) { held = held || st.Action == ModeActivate })
	if held {
		return errors.New("a mode switches on by itself only through a trigger armed on the Modes page")
	}
	return nil
}

// test serves Test now: the run starts in the background; its record and
// any approval appear in the runs and on the approval screen.
func (a *API) test(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	if _, code, err := a.owned(r, id); err != nil {
		httpError(w, code, err.Error())
		return
	}
	var check func(context.Context, spec.Spec) error
	if a.Check != nil {
		check = a.Check
	}
	err := a.Tester.Test(r.Context(), id, check)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "no such automation")
		return
	case errors.Is(err, ErrNotTestable):
		a.auditOnly(r, "automation.test", id, "refused")
		httpError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), ErrNotTestable.Error()+": "))
		return
	case err != nil:
		httpError(w, http.StatusInternalServerError, "the test could not start")
		return
	}
	a.auditOnly(r, "automation.test", id, "ok")
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// auditOnly audits without telling Changed (nothing was saved).
func (a *API) auditOnly(r *http.Request, action string, id int64, result string) {
	_ = a.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: action, Target: "automation:" + strconv.FormatInt(id, 10), Result: result})
}

// dryRun serves "What would have happened?": {spec} of the editor, and the
// automation's id when it is saved. The spec is checked as a save checks
// it; nothing is stored, run or audited.
func (a *API) dryRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID   int64           `json:"id"`
		Spec json.RawMessage `json:"spec"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		httpError(w, http.StatusBadRequest, "body too large")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || b.ID < 0 {
		httpError(w, http.StatusBadRequest, "body must be {spec} or {id, spec}")
		return
	}
	s, err := spec.Parse(b.Spec)
	if err == nil {
		err = userMade(s)
	}
	if err == nil && a.Check != nil {
		err = a.Check(r.Context(), s)
	}
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := a.DryRun(r.Context(), b.ID, s)
	switch {
	case errors.Is(err, ErrDryRunBusy):
		httpError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		httpError(w, http.StatusInternalServerError, "the dry run could not finish")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
