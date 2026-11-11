package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Alert rules are automations (ROADMAP 12.7, plan Q1): WHEN an incident of
// the rule opens THEN notify, through every channel alerts use. The alert
// engine still keeps incidents (open, peak, close); the Alerts page's
// rule form reads and writes these automations through AlertRules, so the
// rules exist once and pass the one validator.

// OriginAlert is the origin of a rule made on the Alerts page or migrated.
const OriginAlert = "alert"

// Options of a rule's automation: an incident is already debounced by its
// duration and hysteresis, so the cooldown is the shortest allowed.
const (
	alertCooldown = 60
	alertPerHour  = 60
)

// RuleSpec is the automation of an alert rule.
func RuleSpec(r store.AlertRule) spec.Spec {
	return spec.Spec{
		Trigger: ruleTrigger(r),
		Then:    []spec.Step{{Step: "notify", Channels: []string{"all"}, Text: "{alert}", Severity: r.Severity}},
		Options: spec.Options{CooldownS: alertCooldown, MaxPerHour: alertPerHour, QuietFrom: r.QuietFrom, QuietTo: r.QuietTo},
	}
}

func ruleTrigger(r store.AlertRule) spec.Trigger {
	return spec.Trigger{Kind: "alert", Metric: r.Metric, Op: r.Op, Threshold: r.Threshold, ForS: r.ForSec, Target: r.Target, Severity: r.Severity}
}

// ruleOf is the rule an alert automation stands for.
func ruleOf(a store.Automation, s spec.Spec) store.AlertRule {
	t := s.Trigger
	return store.AlertRule{ID: a.ID, Name: a.Name, Metric: t.Metric, Target: t.Target, Op: t.Op, Threshold: t.Threshold,
		ForSec: t.ForS, Severity: t.Severity, Enabled: a.Enabled, QuietFrom: s.Options.QuietFrom, QuietTo: s.Options.QuietTo}
}

// AlertDB is what AlertRules needs (store.DB).
type AlertDB interface {
	Automations(ctx context.Context) ([]store.Automation, error)
	GetAutomation(ctx context.Context, id int64) (store.Automation, error)
	CreateAutomations(ctx context.Context, list []store.Automation) ([]int64, error)
	UpdateAutomation(ctx context.Context, a store.Automation) error
	DeleteAutomation(ctx context.Context, id int64) error
}

// AlertRules serves the alert rules from their automations.
type AlertRules struct {
	DB AlertDB
	// Changed is told after a change (the automations engine reloads).
	Changed func()
	Clock   func() time.Time
}

func (r AlertRules) now() int64 {
	if r.Clock != nil {
		return r.Clock().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func (r AlertRules) changed() {
	if r.Changed != nil {
		r.Changed()
	}
}

// AlertRules returns every automation with an alert trigger, as rules.
func (r AlertRules) AlertRules(ctx context.Context) ([]store.AlertRule, error) {
	all, err := r.DB.Automations(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.AlertRule{}
	for _, a := range all {
		s, err := spec.Parse(a.Spec)
		if err != nil || s.Trigger.Kind != "alert" {
			continue
		}
		out = append(out, ruleOf(a, s))
	}
	return out, nil
}

// checked builds and validates a rule's spec.
func checked(s spec.Spec) (json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if _, err := spec.Parse(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// CreateAlertRule adds the rule's automation and returns its ID.
func (r AlertRules) CreateAlertRule(ctx context.Context, rule store.AlertRule) (int64, error) {
	if err := spec.CheckName(rule.Name); err != nil {
		return 0, err
	}
	raw, err := checked(RuleSpec(rule))
	if err != nil {
		return 0, err
	}
	now := r.now()
	ids, err := r.DB.CreateAutomations(ctx, []store.Automation{{Name: rule.Name, Enabled: rule.Enabled, Spec: raw, Origin: OriginAlert, Created: now, Updated: now}})
	if err != nil {
		return 0, err
	}
	r.changed()
	return ids[0], nil
}

// alertAutomation returns the automation id if it has an alert trigger.
func (r AlertRules) alertAutomation(ctx context.Context, id int64) (store.Automation, spec.Spec, error) {
	a, err := r.DB.GetAutomation(ctx, id)
	if err != nil {
		return a, spec.Spec{}, err
	}
	s, err := spec.Parse(a.Spec)
	if err != nil || s.Trigger.Kind != "alert" {
		return a, s, store.ErrNotFound
	}
	return a, s, nil
}

// UpdateAlertRule changes the rule's trigger, name, quiet hours and on/off;
// steps added on the Automations page are kept, and the alert notification
// takes the rule's severity.
func (r AlertRules) UpdateAlertRule(ctx context.Context, rule store.AlertRule) error {
	if err := spec.CheckName(rule.Name); err != nil {
		return err
	}
	a, s, err := r.alertAutomation(ctx, rule.ID)
	if err != nil {
		return err
	}
	s.Trigger = ruleTrigger(rule)
	s.Options.QuietFrom, s.Options.QuietTo = rule.QuietFrom, rule.QuietTo
	for i := range s.Then {
		if s.Then[i].Step == "notify" && s.Then[i].Text == "{alert}" {
			s.Then[i].Severity = rule.Severity
		}
	}
	raw, err := checked(s)
	if err != nil {
		return err
	}
	a.Name, a.Enabled, a.Spec, a.Updated = rule.Name, rule.Enabled, raw, r.now()
	if err := r.DB.UpdateAutomation(ctx, a); err != nil {
		return err
	}
	r.changed()
	return nil
}

// DeleteAlertRule removes the rule's automation; its incidents stay.
func (r AlertRules) DeleteAlertRule(ctx context.Context, id int64) error {
	if _, _, err := r.alertAutomation(ctx, id); err != nil {
		return err
	}
	if err := r.DB.DeleteAutomation(ctx, id); err != nil {
		return err
	}
	r.changed()
	return nil
}

// MigrateAlertRules turns every alert_rules row into its automation, in
// one transaction (store.DB.MigrateAlertRules); it is idempotent, since a
// migrated row is deleted, and runs at every start (a SQL migration may
// add a default rule later).
func MigrateAlertRules(ctx context.Context, db interface {
	MigrateAlertRules(ctx context.Context, convert func(store.AlertRule) (json.RawMessage, error)) (int, error)
}) (int, error) {
	n, err := db.MigrateAlertRules(ctx, func(rule store.AlertRule) (json.RawMessage, error) {
		raw, err := json.Marshal(RuleSpec(rule))
		if err != nil {
			return nil, err
		}
		perr := spec.CheckName(rule.Name)
		if perr == nil {
			_, perr = spec.Parse(raw)
		}
		if perr != nil {
			// Kept, turned off: the Automations page shows why it is invalid.
			return raw, errors.Join(ErrInvalidRule, perr)
		}
		return raw, nil
	})
	if err != nil {
		return n, fmt.Errorf("automations: migrate alert rules: %w", err)
	}
	return n, nil
}

// ErrInvalidRule marks a rule migrated turned off because its automation
// does not pass the validator.
var ErrInvalidRule = errors.New("the rule does not pass the automation validator")
