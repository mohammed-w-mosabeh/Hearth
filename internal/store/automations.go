package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	return tx.Commit()
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
