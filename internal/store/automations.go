package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Automation is one automations row. Spec is stored as validated JSON;
// callers decode it with internal/automations/spec.
type Automation struct {
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Spec    json.RawMessage `json:"spec"`
	Origin  string          `json:"origin"`
	Created int64           `json:"created"`
	Updated int64           `json:"updated"`
}

// Automations returns every automation, by ID.
func (db *DB) Automations(ctx context.Context) ([]Automation, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, enabled, spec, origin, created, updated FROM automations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: automations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Automation
	for rows.Next() {
		a, err := scanAutomation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAutomation(scan func(...any) error) (Automation, error) {
	var a Automation
	var spec string
	if err := scan(&a.ID, &a.Name, &a.Enabled, &spec, &a.Origin, &a.Created, &a.Updated); err != nil {
		return a, fmt.Errorf("store: automation: %w", err)
	}
	a.Spec = json.RawMessage(spec)
	return a, nil
}

// GetAutomation returns one automation, or ErrNotFound.
func (db *DB) GetAutomation(ctx context.Context, id int64) (Automation, error) {
	a, err := scanAutomation(db.QueryRowContext(ctx,
		`SELECT id, name, enabled, spec, origin, created, updated FROM automations WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// CreateAutomations inserts automations in one transaction (an import is
// all or nothing) and returns their IDs.
func (db *DB) CreateAutomations(ctx context.Context, list []Automation) ([]int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: create automations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	ids := make([]int64, 0, len(list))
	for _, a := range list {
		res, err := tx.ExecContext(ctx, `INSERT INTO automations (name, enabled, spec, origin, created, updated) VALUES (?, ?, ?, ?, ?, ?)`,
			a.Name, a.Enabled, string(a.Spec), a.Origin, now, now)
		if err != nil {
			return nil, fmt.Errorf("store: create automation: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// UpdateAutomation replaces an automation's name, enabled flag and spec.
func (db *DB) UpdateAutomation(ctx context.Context, a Automation) error {
	res, err := db.ExecContext(ctx, `UPDATE automations SET name = ?, enabled = ?, spec = ?, updated = ? WHERE id = ?`,
		a.Name, a.Enabled, string(a.Spec), time.Now().UnixMilli(), a.ID)
	if err != nil {
		return fmt.Errorf("store: update automation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAutomationEnabled turns an automation on or off.
func (db *DB) SetAutomationEnabled(ctx context.Context, id int64, on bool) error {
	res, err := db.ExecContext(ctx, `UPDATE automations SET enabled = ?, updated = ? WHERE id = ?`, on, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("store: enable automation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAutomation removes an automation, its runs and its approvals.
func (db *DB) DeleteAutomation(ctx context.Context, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete automation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteAutomationTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteAutomationTx deletes automation id with its runs and approvals.
func deleteAutomationTx(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM automations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete automation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automation_approvals WHERE run_id IN (SELECT id FROM automation_runs WHERE automation_id = ?)`, id); err != nil {
		return fmt.Errorf("store: delete automation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automation_runs WHERE automation_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete automation: %w", err)
	}
	return nil
}

// AutomationRun is one automation_runs row.
type AutomationRun struct {
	ID           int64           `json:"id"`
	AutomationID int64           `json:"automation_id"`
	Started      int64           `json:"started"`
	Finished     int64           `json:"finished"`
	State        string          `json:"state"`
	Record       json.RawMessage `json:"record"`
}

// MaxRunsKept is how many runs an automation keeps.
const MaxRunsKept = 1000

// SaveAutomationRun inserts a run (ID 0) or updates it, and returns its ID.
func (db *DB) SaveAutomationRun(ctx context.Context, r AutomationRun) (int64, error) {
	if r.ID == 0 {
		res, err := db.ExecContext(ctx, `INSERT INTO automation_runs (automation_id, started, finished, state, record) VALUES (?, ?, ?, ?, ?)`,
			r.AutomationID, r.Started, r.Finished, r.State, string(r.Record))
		if err != nil {
			return 0, fmt.Errorf("store: save run: %w", err)
		}
		return res.LastInsertId()
	}
	_, err := db.ExecContext(ctx, `UPDATE automation_runs SET finished = ?, state = ?, record = ? WHERE id = ?`,
		r.Finished, r.State, string(r.Record), r.ID)
	if err != nil {
		return 0, fmt.Errorf("store: save run: %w", err)
	}
	return r.ID, nil
}

// AutomationRuns returns an automation's runs, newest first (0 for all).
func (db *DB) AutomationRuns(ctx context.Context, automation int64, limit int) ([]AutomationRun, error) {
	if limit <= 0 || limit > MaxRunsKept {
		limit = 100
	}
	q, args := `SELECT id, automation_id, started, finished, state, record FROM automation_runs ORDER BY started DESC, id DESC LIMIT ?`, []any{limit}
	if automation > 0 {
		q, args = `SELECT id, automation_id, started, finished, state, record FROM automation_runs WHERE automation_id = ? ORDER BY started DESC, id DESC LIMIT ?`, []any{automation, limit}
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AutomationRun
	for rows.Next() {
		var r AutomationRun
		var rec string
		if err := rows.Scan(&r.ID, &r.AutomationID, &r.Started, &r.Finished, &r.State, &rec); err != nil {
			return nil, fmt.Errorf("store: runs: %w", err)
		}
		r.Record = json.RawMessage(rec)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneAutomationRuns removes runs older than before, and all but the
// newest MaxRunsKept of each automation.
func (db *DB) PruneAutomationRuns(ctx context.Context, before time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM automation_runs WHERE started < ? AND state != 'waiting'`, before.UnixMilli()); err != nil {
		return fmt.Errorf("store: prune runs: %w", err)
	}
	_, err := db.ExecContext(ctx, `DELETE FROM automation_runs WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY automation_id ORDER BY started DESC, id DESC) AS n FROM automation_runs)
		WHERE n > ?)`, MaxRunsKept)
	if err != nil {
		return fmt.Errorf("store: prune runs: %w", err)
	}
	return nil
}

// GetAutomationRun returns one run, or ErrNotFound.
func (db *DB) GetAutomationRun(ctx context.Context, id int64) (AutomationRun, error) {
	var r AutomationRun
	var rec string
	err := db.QueryRowContext(ctx, `SELECT id, automation_id, started, finished, state, record FROM automation_runs WHERE id = ?`, id).
		Scan(&r.ID, &r.AutomationID, &r.Started, &r.Finished, &r.State, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("store: run: %w", err)
	}
	r.Record = json.RawMessage(rec)
	return r, nil
}

// AutomationApproval is one automation_approvals row: the steps of a run
// that wait for the user's click (ROADMAP 12.6). AutomationID comes from
// the run.
type AutomationApproval struct {
	ID           int64           `json:"id"`
	RunID        int64           `json:"run_id"`
	AutomationID int64           `json:"automation_id"`
	Created      int64           `json:"created"`
	Expires      int64           `json:"expires"`
	State        string          `json:"state"` // ApprovalPending, ...
	Steps        json.RawMessage `json:"steps"`
}

// AddAutomationApproval stores a pending approval and returns its ID.
func (db *DB) AddAutomationApproval(ctx context.Context, a AutomationApproval) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO automation_approvals (run_id, created, expires, state, steps) VALUES (?, ?, ?, ?, ?)`,
		a.RunID, a.Created, a.Expires, ApprovalPending, string(a.Steps))
	if err != nil {
		return 0, fmt.Errorf("store: add approval: %w", err)
	}
	return res.LastInsertId()
}

const approvalSelect = `SELECT p.id, p.run_id, COALESCE(r.automation_id, 0), p.created, p.expires, p.state, p.steps
	FROM automation_approvals p LEFT JOIN automation_runs r ON r.id = p.run_id`

func scanAutoApproval(scan func(...any) error) (AutomationApproval, error) {
	var a AutomationApproval
	var steps string
	if err := scan(&a.ID, &a.RunID, &a.AutomationID, &a.Created, &a.Expires, &a.State, &steps); err != nil {
		return a, err
	}
	a.Steps = json.RawMessage(steps)
	return a, nil
}

// AutomationApprovalByID returns one approval, or ErrNotFound.
func (db *DB) AutomationApprovalByID(ctx context.Context, id int64) (AutomationApproval, error) {
	a, err := scanAutoApproval(db.QueryRowContext(ctx, approvalSelect+` WHERE p.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("store: approval: %w", err)
	}
	return a, nil
}

// PendingAutomationApprovals returns the pending approvals, oldest first.
func (db *DB) PendingAutomationApprovals(ctx context.Context) ([]AutomationApproval, error) {
	rows, err := db.QueryContext(ctx, approvalSelect+` WHERE p.state = ? ORDER BY p.created, p.id`, ApprovalPending)
	if err != nil {
		return nil, fmt.Errorf("store: approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AutomationApproval{}
	for rows.Next() {
		a, err := scanAutoApproval(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: approvals: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideAutomationApproval moves a pending approval to state. It returns
// ErrNotFound when no pending approval has that ID, so one decision wins.
func (db *DB) DecideAutomationApproval(ctx context.Context, id int64, state string) error {
	if state == ApprovalPending {
		return errors.New("store: an approval is decided to approved, dismissed or expired")
	}
	res, err := db.ExecContext(ctx, `UPDATE automation_approvals SET state = ? WHERE id = ? AND state = ?`, state, id, ApprovalPending)
	if err != nil {
		return fmt.Errorf("store: decide approval: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneAutomationApprovals deletes decided approvals created before before.
func (db *DB) PruneAutomationApprovals(ctx context.Context, before time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM automation_approvals WHERE state != ? AND created < ?`, ApprovalPending, before.UnixMilli()); err != nil {
		return fmt.Errorf("store: prune approvals: %w", err)
	}
	return nil
}

// MigrateAlertRules moves every alert_rules row into automations (origin
// "alert", ROADMAP 12.7) in one transaction: convert builds each rule's
// spec (an error that wraps a spec is stored turned off; any other error
// stops the migration), incidents are moved to the new IDs, the rows are
// deleted, and the move is audited. It returns how many moved.
func (db *DB) MigrateAlertRules(ctx context.Context, convert func(AlertRule) (json.RawMessage, error)) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	var rules []AlertRule
	for rows.Next() {
		var r AlertRule
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Target, &r.Op, &r.Threshold, &r.ForSec, &r.Severity, &r.Enabled, &r.QuietFrom, &r.QuietTo); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: migrate alert rules: %w", err)
		}
		rules = append(rules, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	if len(rules) == 0 {
		return 0, nil
	}
	// Incidents of rules deleted earlier would point at whatever automation
	// takes their old ID: they point at none.
	if _, err := tx.ExecContext(ctx, `UPDATE alert_events SET rule_id = 0 WHERE rule_id NOT IN (SELECT id FROM alert_rules)`); err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	now := time.Now().UnixMilli()
	for _, r := range rules {
		raw, cerr := convert(r)
		enabled := r.Enabled
		if cerr != nil {
			if raw == nil || !json.Valid(raw) {
				return 0, fmt.Errorf("store: migrate alert rule %d: %w", r.ID, cerr)
			}
			enabled = false
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO automations (name, enabled, spec, origin, created, updated) VALUES (?, ?, ?, 'alert', ?, ?)`,
			r.Name, enabled, string(raw), now, now)
		if err != nil {
			return 0, fmt.Errorf("store: migrate alert rule %d: %w", r.ID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		// Negative while moving, so a new ID equal to an old one is not
		// moved twice.
		if _, err := tx.ExecContext(ctx, `UPDATE alert_events SET rule_id = ? WHERE rule_id = ?`, -id, r.ID); err != nil {
			return 0, fmt.Errorf("store: migrate alert rule %d: %w", r.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, r.ID); err != nil {
			return 0, fmt.Errorf("store: migrate alert rule %d: %w", r.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_events SET rule_id = -rule_id WHERE rule_id < 0`); err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	hash, err := ParamsHash(nil)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (ts, origin, uid, action, target, params_hash, result) VALUES (?, 'local', ?, 'automations.migrate', 'alert-rules', ?, ?)`,
		now, os.Getuid(), hash, fmt.Sprintf("ok: %d rules", len(rules))); err != nil {
		return 0, fmt.Errorf("store: migrate alert rules: %w", err)
	}
	return len(rules), tx.Commit()
}

// SyncModeAutomations makes the automations of mode triggers (origin
// "mode:…", ROADMAP 12.7) be exactly desired, matched by origin, in one
// transaction: the others are deleted, the missing ones added, and a
// change is audited. It returns how many were added and deleted.
func (db *DB) SyncModeAutomations(ctx context.Context, desired []Automation) (created, deleted int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, origin, name, enabled, spec FROM automations WHERE origin LIKE 'mode:%'`)
	if err != nil {
		return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
	}
	have := map[string]int64{}
	held := map[string]Automation{}
	var stale []int64
	for rows.Next() {
		var a Automation
		var spec string
		if err := rows.Scan(&a.ID, &a.Origin, &a.Name, &a.Enabled, &spec); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
		}
		a.Spec = json.RawMessage(spec)
		if _, dup := have[a.Origin]; dup {
			stale = append(stale, a.ID)
			continue
		}
		have[a.Origin], held[a.Origin] = a.ID, a
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
	}
	want := map[string]bool{}
	for _, a := range desired {
		want[a.Origin] = true
		if cur, ok := held[a.Origin]; ok {
			// Made by an older hearthd: replaced, as a new one would be made.
			if cur.Name != a.Name || cur.Enabled != a.Enabled || string(cur.Spec) != string(a.Spec) {
				if err := deleteAutomationTx(ctx, tx, cur.ID); err != nil {
					return 0, 0, err
				}
				deleted++
				delete(have, a.Origin)
			} else {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO automations (name, enabled, spec, origin, created, updated) VALUES (?, ?, ?, ?, ?, ?)`,
			a.Name, a.Enabled, string(a.Spec), a.Origin, a.Created, a.Updated); err != nil {
			return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
		}
		have[a.Origin] = 0
		created++
	}
	for origin, id := range have {
		if !want[origin] && id != 0 {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		if err := deleteAutomationTx(ctx, tx, id); err != nil {
			return 0, 0, err
		}
		deleted++
	}
	if created+deleted == 0 {
		return 0, 0, nil
	}
	hash, err := ParamsHash(nil)
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (ts, origin, uid, action, target, params_hash, result) VALUES (?, 'local', ?, 'automations.sync', 'mode-triggers', ?, ?)`,
		time.Now().UnixMilli(), os.Getuid(), hash, fmt.Sprintf("ok: %d added, %d deleted", created, deleted)); err != nil {
		return 0, 0, fmt.Errorf("store: sync mode automations: %w", err)
	}
	return created, deleted, tx.Commit()
}

// LastAutomationRuns returns each automation's latest run, by automation
// (the Automations list), in one query.
func (db *DB) LastAutomationRuns(ctx context.Context) (map[int64]AutomationRun, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.id, r.automation_id, r.started, r.finished, r.state, r.record FROM automation_runs r
		JOIN (SELECT automation_id, MAX(id) AS id FROM automation_runs GROUP BY automation_id) last ON last.id = r.id`)
	if err != nil {
		return nil, fmt.Errorf("store: last runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]AutomationRun{}
	for rows.Next() {
		var r AutomationRun
		var rec string
		if err := rows.Scan(&r.ID, &r.AutomationID, &r.Started, &r.Finished, &r.State, &rec); err != nil {
			return nil, fmt.Errorf("store: last runs: %w", err)
		}
		r.Record = json.RawMessage(rec)
		out[r.AutomationID] = r
	}
	return out, rows.Err()
}
