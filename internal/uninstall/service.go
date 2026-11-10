package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// KeepOps is how long Recently removed lists an operation.
const KeepOps = 7 * 24 * time.Hour

// Service is Uninstall completely in hearthd: the plan endpoint, the
// uninstall/remove action, Recently removed, and the record's clean-up.
type Service struct {
	Builder Planner
	Engine  *Engine
	DB      *store.DB
	Log     *slog.Logger
}

// Planner builds plans (*Builder).
type Planner interface {
	Build(ctx context.Context, item string) (Plan, error)
}

// maxSelect bounds a selection.
const maxSelect = 4000

func httpJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrBadItem):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNoOwner):
		return http.StatusUnprocessableEntity
	}
	return http.StatusServiceUnavailable
}

// validItem is the shape every item ID has before it is looked up.
func validItem(s string) error {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" || len(s) > 4200 || strings.ContainsAny(s, "\x00\n\r") {
		return ErrBadItem
	}
	if kind != "app" && kind != "tool" && kind != "package" && kind != "game" && extraKinds[kind] == "" {
		return ErrBadItem
	}
	return nil
}

// HandlePlan serves GET /api/v1/uninstall/plan?item=<id>: the full preview,
// read-only.
func (s *Service) HandlePlan(w http.ResponseWriter, r *http.Request) {
	item := r.URL.Query().Get("item")
	if err := validItem(item); err != nil {
		httpJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p, err := s.Builder.Build(r.Context(), item)
	if err != nil {
		httpJSON(w, statusOf(err), map[string]string{"error": err.Error()})
		return
	}
	httpJSON(w, http.StatusOK, p)
}

// HandleOps serves GET /api/v1/uninstall/ops: Recently removed.
func (s *Service) HandleOps(w http.ResponseWriter, r *http.Request) {
	ops, err := s.DB.UninstallOps(r.Context(), time.Now().Add(-KeepOps))
	if err != nil {
		httpJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the history could not be read"})
		return
	}
	out := []json.RawMessage{}
	for _, op := range ops {
		out = append(out, op.Record)
	}
	httpJSON(w, http.StatusOK, out)
}

// Save stores a record.
func (s *Service) Save(ctx context.Context, r Record) error {
	raw, err := r.Marshal()
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return s.DB.SaveUninstallOp(ctx, store.UninstallOp{ID: r.ID, Item: r.Item, Name: r.Name, Created: r.Created, Updated: now, State: r.State, Record: raw})
}

// Run prunes old records hourly until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		if err := s.DB.PruneUninstallOps(ctx, time.Now().Add(-KeepOps)); err != nil && s.Log != nil {
			s.Log.Warn("uninstall: prune", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// state is what the preview hands to the run.
type state struct {
	plan Plan
	rows []Candidate
}

// selection reads params["select"], a JSON list of candidate keys.
func selection(p map[string]string) (map[string]bool, error) {
	raw, ok := p["select"]
	if !ok {
		return nil, errors.New("params.select is required")
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil || len(keys) > maxSelect {
		return nil, errors.New("params.select must be a list of rows")
	}
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out, nil
}

// plan builds the preview for a selection, refusing a selection that names
// a row the plan does not have and checking the typed words.
func (s *Service) plan(ctx context.Context, r actions.Request) (actions.Plan, error) {
	out := actions.Plan{Summary: "uninstall completely", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	sel, err := selection(r.Params)
	if err != nil {
		return out, actions.Errorf(http.StatusBadRequest, "%v", err)
	}
	p, err := s.Builder.Build(ctx, r.Target)
	if err != nil {
		return out, actions.Errorf(statusOf(err), "%v", err)
	}
	out.Summary = "uninstall " + p.Name
	if p.Blocked != "" {
		out.Blocked = append(out.Blocked, actions.Blocked{Item: actions.Item{ID: p.Item, Name: p.Name, Detail: p.Blocked}, Reason: "blocked"})
		return out, nil
	}
	known := map[string]Candidate{}
	for _, c := range p.Candidates {
		known[c.Key()] = c
	}
	for k := range sel {
		c, ok := known[k]
		if !ok {
			return out, actions.Errorf(http.StatusConflict, "the plan changed since the preview; preview again")
		}
		if c.Group == GroupRemains {
			return out, actions.Errorf(http.StatusBadRequest, "%s is not something Hearth removes", c.Name)
		}
	}
	// What the program's own removal takes along always goes with it.
	for _, c := range p.Candidates {
		if c.Group == GroupProgram && c.Kind == KindPackage {
			sel[c.Key()] = true
		}
	}
	rows := Selected(p, sel)
	if !slices.ContainsFunc(rows, func(c Candidate) bool { return c.Group == GroupProgram }) &&
		slices.ContainsFunc(p.Candidates, func(c Candidate) bool { return c.Group == GroupProgram }) {
		return out, actions.Errorf(http.StatusBadRequest, "the program itself must be selected")
	}
	if len(p.Desktop) > 0 && r.Params["desktop"] != DesktopWords {
		return out, actions.Errorf(http.StatusBadRequest, "%s can end your desktop session: type %q", strings.Join(p.Desktop, ", "), DesktopWords)
	}
	var typed []string
	if raw := r.Params["data"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &typed); err != nil {
			return out, actions.Errorf(http.StatusBadRequest, "params.data must be a list of paths")
		}
	}
	for _, c := range rows {
		if c.Warn == "data" && !slices.Contains(typed, c.ID) {
			return out, actions.Errorf(http.StatusBadRequest, "%s may hold data: type its path to remove it", c.ID)
		}
	}
	more := false
	for _, c := range rows {
		out.Items = append(out.Items, actions.Item{ID: c.Key(), Name: c.Name, Detail: c.Group + ": " + c.Reason})
		if c.Group != GroupProgram {
			more = true
		}
	}
	switch {
	case len(p.Desktop) > 0:
		out.Confirm = p.Desktop[0]
	case more:
		out.Confirm = p.Name
	}
	out.State = state{plan: p, rows: rows}
	return out, nil
}

func (s *Service) run(ctx context.Context, _ actions.Request, ap actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	st, ok := ap.State.(state)
	if !ok || len(st.rows) == 0 {
		return res, nil
	}
	rec, err := s.Engine.Run(ctx, st.plan, st.rows)
	for _, step := range rec.Steps {
		if step.Done {
			res.Done++
		} else {
			res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: step.ID, Name: step.Name, Detail: step.Detail}, Reason: step.Step})
		}
	}
	if err != nil && len(res.Failed) == 0 {
		res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: rec.ID, Name: rec.Name, Detail: err.Error()}, Reason: rec.Failed})
	}
	return res, nil
}

// Register adds uninstall/remove.
func (s *Service) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "uninstall", Name: "remove", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if err := validItem(r.Target); err != nil {
				return err
			}
			for k := range r.Params {
				if k != "select" && k != "desktop" && k != "data" {
					return fmt.Errorf("unknown param %q", k)
				}
			}
			return nil
		},
		Plan: s.plan,
		Run:  s.run,
	})
}

// DBRefs edits Hearth's references in its database and mode files.
type DBRefs struct {
	DB    *store.DB
	Modes *modes.Set
}

const favoritesKey = "apps.favorites"

type favorites struct {
	IDs []string `json:"ids"`
}

func (d DBRefs) favorites(ctx context.Context) (favorites, error) {
	var f favorites
	raw, ok, err := d.DB.Setting(ctx, favoritesKey)
	if err != nil || !ok {
		return f, err
	}
	return f, json.Unmarshal(raw, &f)
}

// RemoveFavorite drops id from the favorites.
func (d DBRefs) RemoveFavorite(ctx context.Context, id string) error {
	f, err := d.favorites(ctx)
	if err != nil {
		return err
	}
	f.IDs = slices.DeleteFunc(f.IDs, func(x string) bool { return x == id })
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return d.DB.SetSetting(ctx, favoritesKey, raw)
}

// DeleteAlertRule removes a rule and returns it as it was.
func (d DBRefs) DeleteAlertRule(ctx context.Context, id int64) (store.AlertRule, error) {
	rules, err := d.DB.AlertRules(ctx)
	if err != nil {
		return store.AlertRule{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r, d.DB.DeleteAlertRule(ctx, id)
		}
	}
	return store.AlertRule{}, fmt.Errorf("alert rule %d is gone", id)
}

// TakePlaytime removes a game's tracked time and returns it.
func (d DBRefs) TakePlaytime(ctx context.Context, id string) (store.Playtime, bool, error) {
	return d.DB.TakePlaytime(ctx, id)
}

// RemoveModeRef removes one item or app to open from a mode file.
func (d DBRefs) RemoveModeRef(ctx context.Context, mode, kind, target string) (string, error) {
	if d.Modes == nil {
		return "", errors.New("modes are not available")
	}
	do := ""
	err := d.Modes.Edit(ctx, mode, func(m *modes.Mode) bool {
		if kind == "open" {
			n := len(m.Session.Open)
			m.Session.Open = slices.DeleteFunc(m.Session.Open, func(a string) bool { return a == target })
			return len(m.Session.Open) != n
		}
		for i, it := range m.Items {
			if it.Kind == kind && it.Target == target {
				do = it.Do
				m.Items = slices.Delete(m.Items, i, i+1)
				return true
			}
		}
		return false
	})
	return do, err
}
