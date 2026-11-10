package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// How long an approval waits, and how long a decided one is kept.
const (
	ApprovalTTL  = 12 * time.Hour
	approvalKeep = 30 * 24 * time.Hour
)

// ApprovalDB is what approvals keep (store.DB).
type ApprovalDB interface {
	AddModeApproval(ctx context.Context, a store.ModeApproval) (int64, error)
	ModeApproval(ctx context.Context, id int64) (store.ModeApproval, error)
	PendingModeApprovals(ctx context.Context) ([]store.ModeApproval, error)
	DecideModeApproval(ctx context.Context, id int64, state string, at time.Time) error
	PruneModeApprovals(ctx context.Context, before time.Time) error
	ActiveModes(ctx context.Context) ([]store.ActiveMode, error)
}

// ApprovalConfig configures Approvals.
type ApprovalConfig struct {
	Set *Set
	DB  ApprovalDB
	// Apply runs approved stack steps of an active mode and adds them to its
	// restore list (profiles.Manager.ApplyDeferred in production).
	Apply func(ctx context.Context, modeID string, items []Item) (actions.Result, error)
	Clock func() time.Time
	Log   *slog.Logger
}

// Approvals holds the stack steps automatic switches could not run, until
// the user approves or dismisses them (ROADMAP 10.7, SECURITY S15b). An
// automatic switch never asks for a password; approving is the click that
// may.
type Approvals struct{ cfg ApprovalConfig }

// NewApprovals returns Approvals.
func NewApprovals(cfg ApprovalConfig) *Approvals {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Approvals{cfg: cfg}
}

// Defer records stack steps of mode (the file with hash) that the trigger
// by left for approval.
func (a *Approvals) Defer(ctx context.Context, modeID, hash, by string, items []Item) error {
	for _, it := range items {
		if it.Kind != KindStack || (it.Do != DoStart && it.Do != DoStop) {
			return errors.New("modes: only stack steps wait for approval")
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	_, err = a.cfg.DB.AddModeApproval(ctx, store.ModeApproval{ModeID: modeID, FileHash: hash, Reason: by, Actions: string(raw), CreatedAt: a.cfg.Clock()})
	return err
}

// ApprovalView is a pending approval as the API shows it.
type ApprovalView struct {
	ID        int64     `json:"id"`
	Mode      string    `json:"mode"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"` // the trigger's kind
	Items     []Item    `json:"items"`
	CreatedAt time.Time `json:"created_at"`
}

// Pending lists what waits, oldest first, after expiring what is stale.
func (a *Approvals) Pending(ctx context.Context) ([]ApprovalView, error) {
	if err := a.Expire(ctx); err != nil {
		return nil, err
	}
	list, err := a.cfg.DB.PendingModeApprovals(ctx)
	if err != nil {
		return nil, err
	}
	out := []ApprovalView{}
	for _, ap := range list {
		var items []Item
		if json.Unmarshal([]byte(ap.Actions), &items) != nil {
			continue
		}
		m, _ := a.cfg.Set.Mode(ap.ModeID)
		out = append(out, ApprovalView{ID: ap.ID, Mode: ap.ModeID, Name: m.Name, Reason: ap.Reason, Items: items, CreatedAt: ap.CreatedAt})
	}
	return out, nil
}

// stale reports why a pending approval can no longer be approved, or "".
func (a *Approvals) stale(ap store.ModeApproval, active map[string]bool) string {
	switch m, ok := a.cfg.Set.Mode(ap.ModeID); {
	case !ok || m.Hash != ap.FileHash:
		return "the mode file changed since it was switched on"
	case !active[ap.ModeID]:
		return "the mode is no longer on"
	case a.cfg.Clock().Sub(ap.CreatedAt) > ApprovalTTL:
		return "it waited too long"
	}
	return ""
}

func (a *Approvals) activeSet(ctx context.Context) (map[string]bool, error) {
	list, err := a.cfg.DB.ActiveModes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range list {
		out[m.ModeID] = true
	}
	return out, nil
}

// Expire marks stale approvals expired and forgets old decided ones.
func (a *Approvals) Expire(ctx context.Context) error {
	list, err := a.cfg.DB.PendingModeApprovals(ctx)
	if err != nil {
		return err
	}
	active, err := a.activeSet(ctx)
	if err != nil {
		return err
	}
	now := a.cfg.Clock()
	for _, ap := range list {
		if a.stale(ap, active) != "" {
			if err := a.cfg.DB.DecideModeApproval(ctx, ap.ID, store.ApprovalExpired, now); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
	}
	return a.cfg.DB.PruneModeApprovals(ctx, now.Add(-approvalKeep))
}

// Run expires stale approvals every interval until ctx ends.
func (a *Approvals) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := a.Expire(ctx); err != nil {
				a.cfg.Log.Warn("modes: approvals", "err", err)
			}
		}
	}
}

type approvalState struct {
	ap    store.ModeApproval
	items []Item
}

// Register adds mode/approve and mode/dismiss (target: an approval's ID).
// Approving stops something, so it needs the preview's token; it runs the
// stacks in one batch, the one place a password may be asked.
func (a *Approvals) Register(reg *actions.Registry) {
	validate := func(r actions.Request) error {
		if id, err := strconv.ParseInt(r.Target, 10, 64); err != nil || id <= 0 || len(r.Params) != 0 {
			return errors.New("target must be an approval ID and params empty")
		}
		return nil
	}
	plan := func(verb string) func(context.Context, actions.Request) (actions.Plan, error) {
		return func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			id, _ := strconv.ParseInt(r.Target, 10, 64)
			ap, err := a.cfg.DB.ModeApproval(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				return actions.Plan{}, actions.Errorf(http.StatusNotFound, "no such approval")
			}
			if err != nil {
				return actions.Plan{}, err
			}
			if ap.State != store.ApprovalPending {
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "this approval was already %s", ap.State)
			}
			active, err := a.activeSet(ctx)
			if err != nil {
				return actions.Plan{}, err
			}
			if why := a.stale(ap, active); why != "" {
				_ = a.cfg.DB.DecideModeApproval(ctx, ap.ID, store.ApprovalExpired, a.cfg.Clock())
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "this approval expired: %s", why)
			}
			var items []Item
			if err := json.Unmarshal([]byte(ap.Actions), &items); err != nil {
				return actions.Plan{}, err
			}
			m, _ := a.cfg.Set.Mode(ap.ModeID)
			p := actions.Plan{Summary: fmt.Sprintf("%s %s's waiting apps", verb, m.Name), Items: []actions.Item{}, Blocked: []actions.Blocked{},
				State: approvalState{ap: ap, items: items}}
			for _, it := range items {
				p.Items = append(p.Items, actions.Item{ID: it.Kind + ":" + it.Target, Name: it.Target, Detail: it.Do})
				if verb == "approve" && it.Do == DoStop {
					p.Risk = actions.Destructive
				}
			}
			return p, nil
		}
	}
	reg.Register(actions.Action{Kind: "mode", Name: "approve", Risk: actions.Reversible, Validate: validate, Plan: plan("approve"),
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(approvalState)
			if !ok {
				return actions.Result{}, errors.New("modes: no plan")
			}
			// Decided first, so a second click cannot run it twice.
			if err := a.cfg.DB.DecideModeApproval(ctx, st.ap.ID, store.ApprovalApproved, a.cfg.Clock()); errors.Is(err, store.ErrNotFound) {
				return actions.Result{}, actions.Errorf(http.StatusConflict, "this approval was already decided")
			} else if err != nil {
				return actions.Result{}, err
			}
			return a.cfg.Apply(ctx, st.ap.ModeID, st.items)
		}})
	reg.Register(actions.Action{Kind: "mode", Name: "dismiss", Risk: actions.Safe, Validate: validate, Plan: plan("dismiss"),
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(approvalState)
			if !ok {
				return actions.Result{}, errors.New("modes: no plan")
			}
			if err := a.cfg.DB.DecideModeApproval(ctx, st.ap.ID, store.ApprovalDismissed, a.cfg.Clock()); errors.Is(err, store.ErrNotFound) {
				return actions.Result{}, actions.Errorf(http.StatusConflict, "this approval was already decided")
			} else if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}})
}
