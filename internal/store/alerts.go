package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a row to change does not exist.
var ErrNotFound = errors.New("store: not found")

// AlertRule is one alert_rules row.
type AlertRule struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Target    string  `json:"target"` // one instance, or "" for any
	Op        string  `json:"op"`     // ">" or "<"
	Threshold float64 `json:"threshold"`
	ForSec    int     `json:"for_s"`
	Severity  string  `json:"severity"` // info, warning, critical
	Enabled   bool    `json:"enabled"`
	QuietFrom string  `json:"quiet_from"` // "HH:MM" or ""
	QuietTo   string  `json:"quiet_to"`
}

// AlertEvent is one incident (alert_events row). Times are Unix ms.
type AlertEvent struct {
	ID        int64   `json:"id"`
	RuleID    int64   `json:"rule_id"`
	RuleName  string  `json:"rule_name"`
	Metric    string  `json:"metric"`
	Instance  string  `json:"instance"`
	Severity  string  `json:"severity"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	Value     float64 `json:"value"`
	Peak      float64 `json:"peak"`
	StartedAt int64   `json:"started_at"`
	EndedAt   *int64  `json:"ended_at,omitempty"`
	Notified  bool    `json:"notified"`
}

const ruleCols = `id, name, metric, target, op, threshold, for_s, severity, enabled, quiet_from, quiet_to`

// AlertRuleStore holds alert rules somewhere else than alert_rules.
type AlertRuleStore interface {
	AlertRules(ctx context.Context) ([]AlertRule, error)
	CreateAlertRule(ctx context.Context, r AlertRule) (int64, error)
	UpdateAlertRule(ctx context.Context, r AlertRule) error
	DeleteAlertRule(ctx context.Context, id int64) error
}

// SetAlertRuleStore makes s hold the alert rules from now on: every reader
// and writer of rules (the Alerts page, the alert engine, settings backup,
// uninstall's references) then uses it. hearthd sets the automations
// (ROADMAP 12.7) once at start, after MigrateAlertRules emptied alert_rules.
func (db *DB) SetAlertRuleStore(s AlertRuleStore) { db.rules.Store(&s) }

func (db *DB) ruleStore() AlertRuleStore {
	if p := db.rules.Load(); p != nil {
		return *p
	}
	return nil
}

// AlertRules returns all rules by id.
func (db *DB) AlertRules(ctx context.Context) ([]AlertRule, error) {
	if s := db.ruleStore(); s != nil {
		return s.AlertRules(ctx)
	}
	rows, err := db.QueryContext(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: alert rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AlertRule{}
	for rows.Next() {
		var r AlertRule
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Target, &r.Op, &r.Threshold, &r.ForSec, &r.Severity, &r.Enabled, &r.QuietFrom, &r.QuietTo); err != nil {
			return nil, fmt.Errorf("store: alert rules: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateAlertRule inserts r (its ID is ignored) and returns the new ID.
func (db *DB) CreateAlertRule(ctx context.Context, r AlertRule) (int64, error) {
	if s := db.ruleStore(); s != nil {
		return s.CreateAlertRule(ctx, r)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, quiet_from, quiet_to, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Metric, r.Target, r.Op, r.Threshold, r.ForSec, r.Severity, r.Enabled, r.QuietFrom, r.QuietTo, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: create alert rule: %w", err)
	}
	return res.LastInsertId()
}

// UpdateAlertRule replaces rule r.ID.
func (db *DB) UpdateAlertRule(ctx context.Context, r AlertRule) error {
	if s := db.ruleStore(); s != nil {
		return s.UpdateAlertRule(ctx, r)
	}
	res, err := db.ExecContext(ctx,
		`UPDATE alert_rules SET name = ?, metric = ?, target = ?, op = ?, threshold = ?, for_s = ?, severity = ?,
		 enabled = ?, quiet_from = ?, quiet_to = ?, updated_at = ? WHERE id = ?`,
		r.Name, r.Metric, r.Target, r.Op, r.Threshold, r.ForSec, r.Severity, r.Enabled, r.QuietFrom, r.QuietTo, time.Now().UnixMilli(), r.ID)
	if err != nil {
		return fmt.Errorf("store: update alert rule: %w", err)
	}
	return oneRow(res)
}

// DeleteAlertRule deletes rule id. Its incidents stay in the history.
func (db *DB) DeleteAlertRule(ctx context.Context, id int64) error {
	if s := db.ruleStore(); s != nil {
		return s.DeleteAlertRule(ctx, id)
	}
	res, err := db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete alert rule: %w", err)
	}
	return oneRow(res)
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// OpenAlertEvent records a new incident and returns its ID.
func (db *DB) OpenAlertEvent(ctx context.Context, e AlertEvent) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO alert_events (rule_id, rule_name, metric, instance, severity, op, threshold, value, peak, started_at, notified)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RuleID, e.RuleName, e.Metric, e.Instance, e.Severity, e.Op, e.Threshold, e.Value, e.Peak, e.StartedAt, e.Notified)
	if err != nil {
		return 0, fmt.Errorf("store: open alert event: %w", err)
	}
	return res.LastInsertId()
}

// CloseAlertEvent ends incident id with its peak value.
func (db *DB) CloseAlertEvent(ctx context.Context, id int64, endedAt int64, peak float64) error {
	res, err := db.ExecContext(ctx, `UPDATE alert_events SET ended_at = ?, peak = ? WHERE id = ? AND ended_at IS NULL`, endedAt, peak, id)
	if err != nil {
		return fmt.Errorf("store: close alert event: %w", err)
	}
	return oneRow(res)
}

const eventCols = `id, rule_id, rule_name, metric, instance, severity, op, threshold, value, peak, started_at, ended_at, notified`

func scanEvents(rows *sql.Rows) ([]AlertEvent, error) {
	defer func() { _ = rows.Close() }()
	out := []AlertEvent{}
	for rows.Next() {
		var e AlertEvent
		var ended sql.NullInt64
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.Instance, &e.Severity, &e.Op, &e.Threshold,
			&e.Value, &e.Peak, &e.StartedAt, &ended, &e.Notified); err != nil {
			return nil, fmt.Errorf("store: alert events: %w", err)
		}
		if ended.Valid {
			e.EndedAt = &ended.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OpenAlertEvents returns the incidents that have not ended.
func (db *DB) OpenAlertEvents(ctx context.Context) ([]AlertEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+eventCols+` FROM alert_events WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: open alert events: %w", err)
	}
	return scanEvents(rows)
}

// AlertEvents returns up to limit incidents, newest first, with IDs below
// before (0 for the newest).
func (db *DB) AlertEvents(ctx context.Context, limit int, before int64) ([]AlertEvent, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := db.QueryContext(ctx, `SELECT `+eventCols+` FROM alert_events WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("store: alert events: %w", err)
	}
	return scanEvents(rows)
}
