package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Approvals (ROADMAP 12.6, SECURITY S15d): the first step of a run that
// cannot run unattended, and every step after it, wait as one approval.
// The user sees each step's own preview and confirms them with one click
// (the approval's preview token); the steps then run in order with the
// user there, so polkit may ask. Each action step is previewed again
// right before it runs and runs only if it would affect what was approved.

// FireAlert runs the automation of an alert rule whose incident opened
// (the alert engine's Opened): its quiet hours, cooldown and every other
// gate apply as for any trigger.
//
// It reports whether the rule's automation is loaded; when it is not (the
// engine is starting, or a load failed), the caller notifies as before.
func (e *Engine) FireAlert(ctx context.Context, id int64, instance string, value float64, body string) bool {
	e.mu.Lock()
	var found *loaded
	for i := range e.autos {
		if e.autos[i].ID == id && e.autos[i].spec.Trigger.Kind == "alert" {
			a := e.autos[i]
			found = &a
		}
	}
	e.mu.Unlock()
	if found == nil {
		return false
	}
	// The instance and the description come from the machine (unit and
	// container names): cleaned and masked before a run record keeps them.
	e.Fire(ctx, *found, Event{Kind: "alert", Instance: Mask(clean(instance)), Value: value, Alert: Mask(clean(body)), Time: e.cfg.Clock()}, "trigger")
	return true
}

// Bounds of an approval.
const (
	ApprovalTTL  = 12 * time.Hour
	approvalKeep = 30 * 24 * time.Hour
)

// Parked is what an approval holds.
type Parked struct {
	Steps   []spec.Step `json:"steps"`
	Trigger Event       `json:"trigger"`
	// Updated is the automation's as it ran: an edit since expires it.
	Updated int64 `json:"updated"`
}

// park stores the parked steps of a waiting run and tells the user. A
// failure is recorded on the run, which then never waits forever.
func (e *Engine) park(ctx context.Context, a loaded, runID int64, rec RunRecord) {
	if runID == 0 || len(rec.parked) == 0 {
		return
	}
	raw, err := json.Marshal(Parked{Steps: rec.parked, Trigger: rec.Trigger, Updated: a.Updated})
	if err == nil {
		now := e.cfg.Clock()
		_, err = e.cfg.Store.AddAutomationApproval(ctx, store.AutomationApproval{RunID: runID, Created: now.UnixMilli(),
			Expires: now.Add(ApprovalTTL).UnixMilli(), Steps: raw})
	}
	if err != nil {
		e.cfg.Log.Warn("automations: park", "id", a.ID, "err", err)
		e.finish(ctx, runID, RunFailed, "the approval could not be stored", nil)
		return
	}
	if e.cfg.Notify != nil {
		// Words only: the link opens Hearth, where the steps are previewed
		// and approved; nothing runs on a click of the notification.
		e.cfg.Notify(ctx, "Waiting for your approval",
			fmt.Sprintf("%s: %d steps wait for you. Open Hearth, Automations, to review them.", oneLine(a.Name), len(rec.parked)))
	}
}

// finish ends a waiting run: its waiting steps become what happened
// (results, appended), or are marked skipped with reason.
func (e *Engine) finish(ctx context.Context, runID int64, state, reason string, results []StepResult) {
	run, err := e.cfg.Store.GetAutomationRun(ctx, runID)
	if err != nil {
		return
	}
	var rec RunRecord
	if json.Unmarshal(run.Record, &rec) != nil {
		return
	}
	kept := rec.Steps[:0]
	for _, s := range rec.Steps {
		switch {
		case s.Result != "waiting":
			kept = append(kept, s)
		case results == nil:
			s.Result, s.Detail = "skipped", reason
			kept = append(kept, s)
		}
	}
	rec.Steps = append(kept, results...)
	if reason != "" {
		rec.Reason = reason
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	run.State, run.Record, run.Finished = state, raw, e.cfg.Clock().UnixMilli()
	if _, err := e.cfg.Store.SaveAutomationRun(ctx, run); err != nil {
		e.cfg.Log.Warn("automations: record run", "run", runID, "err", err)
	}
}

// stale says why a pending approval can no longer be approved, or "".
func (e *Engine) stale(ctx context.Context, ap store.AutomationApproval, p Parked) (string, store.Automation) {
	a, err := e.cfg.Store.GetAutomation(ctx, ap.AutomationID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "the automation was deleted", a
	case err != nil:
		return "", a
	case e.cfg.Clock().UnixMilli() >= ap.Expires:
		return "it waited too long", a
	case !a.Enabled:
		return "the automation was turned off", a
	case a.Updated != p.Updated:
		return "the automation was edited", a
	}
	return "", a
}

func decodeParked(ap store.AutomationApproval) (Parked, error) {
	var p Parked
	if err := json.Unmarshal(ap.Steps, &p); err != nil || len(p.Steps) == 0 {
		return p, errors.New("automations: an approval could not be read")
	}
	return p, nil
}

// expire marks ap expired and ends its run.
func (e *Engine) expire(ctx context.Context, ap store.AutomationApproval, why string) {
	if err := e.cfg.Store.DecideAutomationApproval(ctx, ap.ID, store.ApprovalExpired); err != nil {
		return
	}
	e.finish(ctx, ap.RunID, RunSkipped, "not approved: "+why, nil)
	_ = e.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.Origin("automation:" + strconv.FormatInt(ap.AutomationID, 10)),
		Action: "automation.approval", Target: "approval:" + strconv.FormatInt(ap.ID, 10), Result: "expired: " + why})
}

// ExpireApprovals expires the pending approvals that went stale and
// forgets old decided ones. While paused, approvals stay (they are refused
// until resumed).
func (e *Engine) ExpireApprovals(ctx context.Context) error {
	list, err := e.cfg.Store.PendingAutomationApprovals(ctx)
	if err != nil {
		return err
	}
	for _, ap := range list {
		p, err := decodeParked(ap)
		if err != nil {
			e.expire(ctx, ap, "it could not be read")
			continue
		}
		if why, _ := e.stale(ctx, ap, p); why != "" {
			e.expire(ctx, ap, why)
		}
	}
	return e.cfg.Store.PruneAutomationApprovals(ctx, e.cfg.Clock().Add(-approvalKeep))
}

// ApprovalView is a pending approval as the API lists it.
type ApprovalView struct {
	ID           int64        `json:"id"`
	AutomationID int64        `json:"automation_id"`
	Name         string       `json:"name"`
	RunID        int64        `json:"run_id"`
	Created      int64        `json:"created"`
	Expires      int64        `json:"expires"`
	Steps        []StepResult `json:"steps"` // step, action, target, label
}

// Approvals lists the pending approvals that can still be approved.
func (e *Engine) Approvals(ctx context.Context) ([]ApprovalView, error) {
	list, err := e.cfg.Store.PendingAutomationApprovals(ctx)
	if err != nil {
		return nil, err
	}
	out := []ApprovalView{}
	for _, ap := range list {
		p, err := decodeParked(ap)
		if err != nil {
			continue
		}
		why, a := e.stale(ctx, ap, p)
		if why != "" {
			continue
		}
		v := ApprovalView{ID: ap.ID, AutomationID: ap.AutomationID, Name: a.Name, RunID: ap.RunID, Created: ap.Created, Expires: ap.Expires, Steps: []StepResult{}}
		walk(p.Steps, "", func(path string, st spec.Step) {
			v.Steps = append(v.Steps, StepResult{Step: st.Step, Action: st.Action, Target: st.Target, Label: st.Label, Result: "waiting", Detail: path})
		})
		out = append(out, v)
	}
	return out, nil
}

// walk visits steps and the steps of their branches, each with its path
// ("2", "2.then.1").
func walk(steps []spec.Step, prefix string, fn func(path string, st spec.Step)) {
	for i, st := range steps {
		path := prefix + strconv.Itoa(i+1)
		fn(path, st)
		walk(st.ThenDo, path+".then.", fn)
		walk(st.ElseDo, path+".else.", fn)
	}
}

type approveState struct {
	ap     store.AutomationApproval
	parked Parked
	auto   store.Automation
	plans  map[string]actions.Plan // by step path
}

// RegisterApprovals adds automation/approve and automation/dismiss
// (target: an approval's ID). Approving previews every parked step by its
// own action and needs the preview's token whenever one of them is
// destructive.
func (e *Engine) RegisterApprovals(reg *actions.Registry) {
	validate := func(r actions.Request) error {
		if id, err := strconv.ParseInt(r.Target, 10, 64); err != nil || id <= 0 || strconv.FormatInt(id, 10) != r.Target || len(r.Params) != 0 {
			return errors.New("target must be an approval ID and params empty")
		}
		return nil
	}
	load := func(ctx context.Context, r actions.Request, approving bool) (approveState, error) {
		id, _ := strconv.ParseInt(r.Target, 10, 64)
		ap, err := e.cfg.Store.AutomationApprovalByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return approveState{}, actions.Errorf(http.StatusNotFound, "no such approval")
		}
		if err != nil {
			return approveState{}, err
		}
		if ap.State != store.ApprovalPending {
			return approveState{}, actions.Errorf(http.StatusConflict, "this approval was already %s", ap.State)
		}
		p, err := decodeParked(ap)
		if err != nil {
			e.expire(ctx, ap, "it could not be read")
			return approveState{}, actions.Errorf(http.StatusConflict, "this approval could not be read")
		}
		why, a := e.stale(ctx, ap, p)
		if why != "" {
			e.expire(ctx, ap, why)
			return approveState{}, actions.Errorf(http.StatusConflict, "this approval expired: %s", why)
		}
		if approving && e.Paused(ctx) {
			return approveState{}, actions.Errorf(http.StatusConflict, "all automations are paused: resume them to approve")
		}
		return approveState{ap: ap, parked: p, auto: a, plans: map[string]actions.Plan{}}, nil
	}
	reg.Register(actions.Action{Kind: "automation", Name: "approve", Risk: actions.Reversible, Authority: actions.AuthorityPolkit,
		Validate: validate,
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			st, err := load(ctx, r, true)
			if err != nil {
				return actions.Plan{}, err
			}
			p := actions.Plan{Summary: "run the waiting steps of " + st.auto.Name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
			var failed error
			walk(st.parked.Steps, "", func(path string, s spec.Step) {
				if failed != nil {
					return
				}
				it := actions.Item{ID: path, Name: stepName(s)}
				switch s.Step {
				case "action":
					req := actions.Request{Target: s.Target, Params: s.Params}
					if why := e.protect(s); why != "" {
						failed = fmt.Errorf("step %s: refused: %s", path, why)
						return
					}
					sp, risk, err := e.cfg.Registry.PlanFor(ctx, s.Action, req)
					if err != nil {
						failed = fmt.Errorf("step %s: %s", path, clean(err.Error()))
						return
					}
					if why := actions.Refusal(sp); why != "" {
						failed = fmt.Errorf("step %s: refused: %s", path, why)
						return
					}
					st.plans[path] = sp
					it.Detail = sp.Summary
					if n := len(sp.Items); n > 0 {
						it.Detail += fmt.Sprintf(" (%d items)", n)
					}
					if risk == actions.Destructive {
						p.Risk = actions.Destructive
					}
				case "notify":
					it.Detail = "notify by " + strings.Join(s.Channels, ", ")
				case "wait":
					it.Detail = fmt.Sprintf("wait %d s", s.Seconds)
				case "check":
					it.Detail = fmt.Sprintf("if %s of %s is %s", strings.ReplaceAll(s.What, "_", " "), s.Target, s.Expect)
				}
				p.Items = append(p.Items, it)
			})
			if failed != nil {
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "%s", failed.Error())
			}
			p.State = st
			return p, nil
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(approveState)
			if !ok {
				return actions.Result{}, errors.New("automations: no plan")
			}
			// Decided first, so a second click cannot run it twice.
			if err := e.cfg.Store.DecideAutomationApproval(ctx, st.ap.ID, store.ApprovalApproved); errors.Is(err, store.ErrNotFound) {
				return actions.Result{}, actions.Errorf(http.StatusConflict, "this approval was already decided")
			} else if err != nil {
				return actions.Result{}, err
			}
			e.resume(context.WithoutCancel(ctx), st)
			return actions.Result{Done: len(st.parked.Steps), Failed: []actions.Blocked{}}, nil
		}})
	reg.Register(actions.Action{Kind: "automation", Name: "dismiss", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: validate,
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			st, err := load(ctx, r, false)
			if err != nil {
				return actions.Plan{}, err
			}
			return actions.Plan{Summary: "dismiss the waiting steps of " + st.auto.Name, Items: []actions.Item{{ID: r.Target, Name: st.auto.Name}},
				Blocked: []actions.Blocked{}, State: st}, nil
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(approveState)
			if !ok {
				return actions.Result{}, errors.New("automations: no plan")
			}
			if err := e.cfg.Store.DecideAutomationApproval(ctx, st.ap.ID, store.ApprovalDismissed); errors.Is(err, store.ErrNotFound) {
				return actions.Result{}, actions.Errorf(http.StatusConflict, "this approval was already decided")
			} else if err != nil {
				return actions.Result{}, err
			}
			e.finish(ctx, st.ap.RunID, RunSkipped, "not approved: dismissed", nil)
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}})
}

func stepName(s spec.Step) string {
	if s.Label != "" {
		return s.Label
	}
	if s.Step == "action" {
		return strings.TrimSpace(s.Action + " " + s.Target)
	}
	return s.Step
}

func (e *Engine) protect(s spec.Step) string {
	if e.cfg.Protect == nil {
		return ""
	}
	return e.cfg.Protect(s.Action, s.Target)
}

// resume runs approved steps in the background with the user there (not
// unattended), then ends the run.
func (e *Engine) resume(ctx context.Context, st approveState) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		a := loaded{Automation: st.auto}
		if sp, err := spec.Parse(st.auto.Spec); err == nil {
			a.spec = sp
		}
		rec := RunRecord{Trigger: st.parked.Trigger, Steps: []StepResult{}}
		state := e.approvedSteps(ctx, a, st.parked.Steps, "", st.plans, &rec)
		e.finish(ctx, st.ap.RunID, state, "", rec.Steps)
		_ = e.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal,
			Action: "automation.approval", Target: "approval:" + strconv.FormatInt(st.ap.ID, 10), Result: "approved: " + state})
	}()
}

// approvedSteps runs steps as approved. An action step runs only as it was
// previewed (Registry.RunApproved); a failure stops, as in an unattended
// run.
func (e *Engine) approvedSteps(ctx context.Context, a loaded, steps []spec.Step, prefix string, plans map[string]actions.Plan, rec *RunRecord) string {
	by := "automation:" + strconv.FormatInt(a.ID, 10)
	for i, st := range steps {
		path := prefix + strconv.Itoa(i+1)
		start := e.cfg.Clock()
		res := StepResult{Step: st.Step, Action: st.Action, Target: st.Target, Label: st.Label, Result: "ok", Detail: "approved"}
		var err error
		switch st.Step {
		case "action":
			if why := e.protect(st); why != "" {
				err = errors.New("refused: " + why)
				break
			}
			plan, ok := plans[path]
			if !ok {
				err = errors.New("it was not previewed")
				break
			}
			var r actions.Result
			r, err = e.cfg.Registry.RunApproved(ctx, st.Action, actions.Request{Target: st.Target, Params: st.Params}, plan, by)
			if err == nil && len(r.Failed) > 0 {
				err = fmt.Errorf("%s: %s", r.Failed[0].Reason, r.Failed[0].Detail)
			}
			if a.spec.Options.CooldownS > 0 {
				e.tag(a.ID, st.Target, e.cfg.Clock().Add(time.Duration(a.spec.Options.CooldownS)*time.Second))
			}
		case "wait":
			err = e.cfg.Sleep(ctx, time.Duration(st.Seconds)*time.Second)
		case "notify":
			if e.cfg.Steps == nil {
				err = errors.New("notifications are not available")
			} else {
				err = e.cfg.Steps.Notify(ctx, st, rec.Trigger, a.Name)
			}
		case "check":
			var pass bool
			if e.cfg.Steps == nil {
				err = errors.New("checks are not available")
			} else if pass, err = e.cfg.Steps.Check(ctx, st); err == nil {
				branch, name := st.ElseDo, "else"
				if pass {
					branch, name = st.ThenDo, "then"
				}
				res.Detail = name
				res.Duration = e.cfg.Clock().Sub(start).Milliseconds()
				rec.Steps = append(rec.Steps, res)
				if state := e.approvedSteps(ctx, a, branch, path+"."+name+".", plans, rec); state != RunDone {
					return state
				}
				continue
			}
		}
		res.Duration = e.cfg.Clock().Sub(start).Milliseconds()
		if err != nil {
			res.Result, res.Detail = "failed", clean(err.Error())
			rec.Steps = append(rec.Steps, res)
			if st.Step == "action" && st.Continue && e.mayContinue(st) {
				continue
			}
			return RunFailed
		}
		rec.Steps = append(rec.Steps, res)
	}
	return RunDone
}
