package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

// Authority says whether running an action can ask for a password
// (SECURITY S15d). An automation runs a step unattended only when its
// authority is none; everything else waits for the user's approval.
type Authority string

const (
	// AuthorityNone: runs as the user; nothing in it can ask polkit.
	AuthorityNone Authority = "none"
	// AuthorityPolkit: may ask, through systemd, the broker, PackageKit,
	// snapd or a system service. The default for anything not listed.
	AuthorityPolkit Authority = "polkit"
)

// noPassword are the actions that run as the user and can never ask for
// a password, each checked against its code: the user's own processes,
// apps, audio, autostart entries and session settings; power profiles and
// brightness (non-interactive calls the active session is allowed);
// NetworkManager connection switches (made without interaction when
// unattended, S15d); Hearth's own listener, backup and the trash.
var noPassword = map[string]bool{
	"proc/terminate": true, "proc/kill": true, "proc/freeze": true, "proc/resume": true, "proc/renice": true, "proc/ionice": true,
	"app/launch": true, "app/close": true, "game/launch": true,
	"audio/volume": true, "audio/mute": true, "audio/default": true,
	"autostart/set": true, "autostart/add": true,
	"session/wallpaper": true, "session/wallpaper-uris": true, "session/dnd": true,
	"power/profile": true, "system/brightness": true,
	"network/connect": true, "network/disconnect": true, "wifi/scan": true, "wifi/radio": true,
	"proxy/listen": true, "proxy/route": true,
	"hearth/backup": true,
	"space/trash":   true, "space/empty-trash": true,
	"mode/arm": true, "mode/disarm": true, "mode/dismiss": true,
	"npm/remove": true, "cargo/remove": true, "gobin/remove": true,
}

// userScoped are actions whose target names a scope first: "user/..." runs
// through the user's own manager (or installation) and never asks;
// "system/..." may.
var userScoped = map[string]bool{
	"unit/start": true, "unit/stop": true, "unit/restart": true, "unit/enable": true, "unit/disable": true,
	"unit/mask": true, "unit/unmask": true, "unit/override": true, "unit/cap": true,
	"job/run": true, "job/pause": true, "job/resume": true,
	"flatpak/remove": true,
}

// AuthorityOf is the authority of running key on r: the action's own
// declaration when it has one, else the table above, else polkit.
func (r *Registry) AuthorityOf(key string, req Request) Authority {
	a, ok := r.Lookup(key)
	if !ok {
		return AuthorityPolkit
	}
	if a.AuthorityFor != nil {
		return a.AuthorityFor(req)
	}
	if a.Authority != "" {
		return a.Authority
	}
	return DefaultAuthority(key, req.Target)
}

// DefaultAuthority is the table's answer for key and target.
func DefaultAuthority(key, target string) Authority {
	if noPassword[key] {
		return AuthorityNone
	}
	if userScoped[key] && strings.HasPrefix(target, "user/") {
		return AuthorityNone
	}
	return AuthorityPolkit
}

// Classified lists the actions the table names, for a test that each is
// a registered action (a renamed one would silently become polkit).
func Classified() []string {
	out := make([]string, 0, len(noPassword)+len(userScoped))
	for k := range noPassword {
		out = append(out, k)
	}
	for k := range userScoped {
		out = append(out, k)
	}
	return out
}

// ErrNeedsApproval is why a step cannot run with nobody at the screen: it
// is destructive, needs a typed confirmation, or may ask for a password.
var ErrNeedsApproval = errors.New("needs your approval")

// Unattended is how a step ran with nobody at the screen.
type Unattended struct {
	Result Result
	Plan   Plan
}

// RunUnattended runs key on req for an automation (SECURITY S15d): only if
// its effective risk is safe or reversible, its plan asks for no typed
// confirmation and its authority is none; otherwise ErrNeedsApproval and
// nothing runs. It runs under the unattended mark (so nothing in it can
// ask polkit) and is audited with origin by, as a request is.
func (r *Registry) RunUnattended(ctx context.Context, key string, req Request, by string) (Unattended, error) {
	a, ok := r.Lookup(key)
	if !ok {
		return Unattended{}, fmt.Errorf("no action %s", key)
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	if err := a.Validate(req); err != nil {
		return Unattended{}, err
	}
	ctx = unattended.With(ctx)
	plan, err := a.Plan(ctx, req)
	if err != nil {
		return Unattended{}, err
	}
	if effective(a, plan) == Destructive || plan.Confirm != "" || r.AuthorityOf(key, req) != AuthorityNone {
		return Unattended{Plan: plan}, ErrNeedsApproval
	}
	res, runErr := a.Run(ctx, req, plan)
	outcome := "ok"
	switch {
	case runErr != nil:
		outcome = "error: " + runErr.Error()
	case len(res.Failed) > 0:
		outcome = fmt.Sprintf("partial: %d done, %d failed", res.Done, len(res.Failed))
	}
	if err := r.audit.Audit(ctx, store.AuditEntry{Origin: store.Origin(by), UID: os.Getuid(), Action: a.Kind + "." + a.Name,
		Target: req.Target, Params: req.Params, Result: outcome}); err != nil {
		return Unattended{Result: res, Plan: plan}, errors.New("action ran but could not be written to the audit log")
	}
	return Unattended{Result: res, Plan: plan}, runErr
}

// PlanFor previews key on req without running it (approvals, dry runs).
func (r *Registry) PlanFor(ctx context.Context, key string, req Request) (Plan, Risk, error) {
	a, ok := r.Lookup(key)
	if !ok {
		return Plan{}, "", fmt.Errorf("no action %s", key)
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	if err := a.Validate(req); err != nil {
		return Plan{}, "", err
	}
	plan, err := a.Plan(ctx, req)
	if err != nil {
		return Plan{}, "", err
	}
	return plan, effective(a, plan), nil
}

// protectedReasons are the blocked reasons that mean a target S16 or S15a
// protects: critical units, Hearth itself, Essential, Protected and
// required packages, the session, the system's own processes, the
// browser showing Hearth.
var protectedReasons = map[string]bool{
	"critical": true, "essential": true, "protected": true, "required": true,
	"self": true, "session": true, "system": true, "browser": true,
}

// Refusal says why an automation may never hold a step with this plan
// (SECURITY S15d), or "": it needs a typed confirmation, or it aims at a
// protected target. Checked at save and again before each run.
func Refusal(p Plan) string {
	if p.Confirm != "" {
		return "needs a typed confirmation, which an automation cannot give"
	}
	for _, b := range p.Blocked {
		if protectedReasons[b.Reason] {
			name := b.Name
			if name == "" {
				name = b.ID
			}
			return fmt.Sprintf("%s is protected (%s)", name, b.Reason)
		}
	}
	return ""
}
