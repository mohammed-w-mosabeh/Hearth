package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ModeArming is one armed trigger of a mode file (SECURITY S15b).
type ModeArming struct {
	ModeID   string
	Trigger  int
	FileHash string
	ArmedAt  time.Time
}

// ActiveMode is the mode switched on now; Snapshot is JSON.
type ActiveMode struct {
	ModeID   string
	FileHash string
	Since    time.Time
	By       string
	Snapshot string
}

// Approval states.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalDismissed = "dismissed"
	ApprovalExpired   = "expired"
)

// ModeApproval is stack work an automatic trigger left for a click; Actions
// is JSON. DecidedAt is zero while it is pending.
type ModeApproval struct {
	ID        int64
	ModeID    string
	FileHash  string
	Reason    string
	Actions   string
	CreatedAt time.Time
	State     string
	DecidedAt time.Time
}

// ArmModeTrigger arms trigger of a mode for the file with hash, replacing an
// earlier arming of it.
func (db *DB) ArmModeTrigger(ctx context.Context, a ModeArming) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO mode_arming (mode_id, trigger_idx, file_hash, armed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (mode_id, trigger_idx) DO UPDATE SET file_hash = excluded.file_hash, armed_at = excluded.armed_at`,
		a.ModeID, a.Trigger, a.FileHash, a.ArmedAt.UnixMilli()); err != nil {
		return fmt.Errorf("store: arm mode trigger: %w", err)
	}
	return nil
}

// DisarmModeTrigger disarms one trigger; disarming one not armed is no error.
func (db *DB) DisarmModeTrigger(ctx context.Context, modeID string, trigger int) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM mode_arming WHERE mode_id = ? AND trigger_idx = ?`, modeID, trigger); err != nil {
		return fmt.Errorf("store: disarm mode trigger: %w", err)
	}
	return nil
}

// DisarmModeStale disarms every trigger of modeID armed for another file than
// hash, and returns how many; an empty hash (the file is gone or invalid)
// disarms them all.
func (db *DB) DisarmModeStale(ctx context.Context, modeID, hash string) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM mode_arming WHERE mode_id = ? AND file_hash != ?`, modeID, hash)
	if err != nil {
		return 0, fmt.Errorf("store: disarm mode: %w", err)
	}
	return res.RowsAffected()
}

// ModeArmings lists every armed trigger.
func (db *DB) ModeArmings(ctx context.Context) ([]ModeArming, error) {
	rows, err := db.QueryContext(ctx, `SELECT mode_id, trigger_idx, file_hash, armed_at FROM mode_arming ORDER BY mode_id, trigger_idx`)
	if err != nil {
		return nil, fmt.Errorf("store: mode armings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ModeArming{}
	for rows.Next() {
		var a ModeArming
		var at int64
		if err := rows.Scan(&a.ModeID, &a.Trigger, &a.FileHash, &at); err != nil {
			return nil, fmt.Errorf("store: mode armings: %w", err)
		}
		a.ArmedAt = time.UnixMilli(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActiveModes lists the active modes, oldest first.
func (db *DB) ActiveModes(ctx context.Context) ([]ActiveMode, error) {
	rows, err := db.QueryContext(ctx, `SELECT mode_id, file_hash, since, activated_by, snapshot FROM mode_active ORDER BY since, mode_id`)
	if err != nil {
		return nil, fmt.Errorf("store: active modes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ActiveMode{}
	for rows.Next() {
		var m ActiveMode
		var since int64
		if err := rows.Scan(&m.ModeID, &m.FileHash, &since, &m.By, &m.Snapshot); err != nil {
			return nil, fmt.Errorf("store: active modes: %w", err)
		}
		m.Since = time.UnixMilli(since)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetActiveMode records m as active, replacing an earlier record of it.
func (db *DB) SetActiveMode(ctx context.Context, m ActiveMode) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO mode_active (mode_id, file_hash, since, activated_by, snapshot) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mode_id) DO UPDATE SET file_hash = excluded.file_hash, since = excluded.since,
		activated_by = excluded.activated_by, snapshot = excluded.snapshot`,
		m.ModeID, m.FileHash, m.Since.UnixMilli(), m.By, m.Snapshot); err != nil {
		return fmt.Errorf("store: set active mode: %w", err)
	}
	return nil
}

// ClearActiveMode records that modeID is no longer active.
func (db *DB) ClearActiveMode(ctx context.Context, modeID string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM mode_active WHERE mode_id = ?`, modeID); err != nil {
		return fmt.Errorf("store: clear active mode: %w", err)
	}
	return nil
}

// ModeNumbers maps mode IDs to their numbers, giving a number to each of
// ids that has none yet.
func (db *DB) ModeNumbers(ctx context.Context, ids []string) (map[string]int64, error) {
	for _, id := range ids {
		if _, err := db.ExecContext(ctx, `INSERT INTO mode_ids (mode_id) VALUES (?) ON CONFLICT (mode_id) DO NOTHING`, id); err != nil {
			return nil, fmt.Errorf("store: number mode: %w", err)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, mode_id FROM mode_ids`)
	if err != nil {
		return nil, fmt.Errorf("store: mode numbers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var n int64
		var id string
		if err := rows.Scan(&n, &id); err != nil {
			return nil, fmt.Errorf("store: mode numbers: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AdoptedProfile is a profile whose definition now lives in a mode file.
type AdoptedProfile struct {
	ProfileID int64
	ModeID    string
	Active    *ActiveMode // what restores it, when it was active
	Arm       *ModeArming // its game trigger, when it was on
}

// AdoptProfile moves a profile's state to mode a.ModeID in one transaction:
// the mode takes the profile's number, its activation and game trigger
// carry over, and the profile row goes, so adopting is done once.
func (db *DB) AdoptProfile(ctx context.Context, a AdoptedProfile) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: adopt profile: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `INSERT INTO mode_ids (id, mode_id) VALUES (?, ?)`, a.ProfileID, a.ModeID); err != nil {
		return fmt.Errorf("store: adopt profile: %w", err)
	}
	if m := a.Active; m != nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO mode_active (mode_id, file_hash, since, activated_by, snapshot) VALUES (?, ?, ?, ?, ?)`,
			a.ModeID, m.FileHash, m.Since.UnixMilli(), m.By, m.Snapshot); err != nil {
			return fmt.Errorf("store: adopt profile: %w", err)
		}
	}
	if r := a.Arm; r != nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO mode_arming (mode_id, trigger_idx, file_hash, armed_at) VALUES (?, ?, ?, ?)`,
			a.ModeID, r.Trigger, r.FileHash, r.ArmedAt.UnixMilli()); err != nil {
			return fmt.Errorf("store: adopt profile: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, a.ProfileID)
	if err != nil {
		return fmt.Errorf("store: adopt profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		err = ErrNotFound
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: adopt profile: %w", err)
	}
	return nil
}

// AddModeApproval stores a pending approval and returns its ID.
func (db *DB) AddModeApproval(ctx context.Context, a ModeApproval) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO mode_approvals (mode_id, file_hash, reason, actions, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.ModeID, a.FileHash, a.Reason, a.Actions, a.CreatedAt.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: add mode approval: %w", err)
	}
	return res.LastInsertId()
}

const approvalColumns = `id, mode_id, file_hash, reason, actions, created_at, state, decided_at`

func scanApproval(scan func(...any) error) (ModeApproval, error) {
	var a ModeApproval
	var created int64
	var decided sql.NullInt64
	if err := scan(&a.ID, &a.ModeID, &a.FileHash, &a.Reason, &a.Actions, &created, &a.State, &decided); err != nil {
		return a, err
	}
	a.CreatedAt = time.UnixMilli(created)
	if decided.Valid {
		a.DecidedAt = time.UnixMilli(decided.Int64)
	}
	return a, nil
}

// ModeApproval returns approval id, or ErrNotFound.
func (db *DB) ModeApproval(ctx context.Context, id int64) (ModeApproval, error) {
	a, err := scanApproval(db.QueryRowContext(ctx, `SELECT `+approvalColumns+` FROM mode_approvals WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("store: mode approval: %w", err)
	}
	return a, nil
}

// PendingModeApprovals lists the approvals still waiting, oldest first.
func (db *DB) PendingModeApprovals(ctx context.Context) ([]ModeApproval, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+approvalColumns+` FROM mode_approvals WHERE state = ? ORDER BY created_at, id`, ApprovalPending)
	if err != nil {
		return nil, fmt.Errorf("store: mode approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ModeApproval{}
	for rows.Next() {
		a, err := scanApproval(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: mode approvals: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideModeApproval moves a pending approval to state. It returns
// ErrNotFound when no pending approval has that ID, so one decision wins.
func (db *DB) DecideModeApproval(ctx context.Context, id int64, state string, at time.Time) error {
	if state == ApprovalPending {
		return errors.New("store: an approval is decided to approved, dismissed or expired")
	}
	res, err := db.ExecContext(ctx, `UPDATE mode_approvals SET state = ?, decided_at = ? WHERE id = ? AND state = ?`,
		state, at.UnixMilli(), id, ApprovalPending)
	if err != nil {
		return fmt.Errorf("store: decide mode approval: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneModeApprovals deletes decided approvals older than before.
func (db *DB) PruneModeApprovals(ctx context.Context, before time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM mode_approvals WHERE state != ? AND decided_at < ?`, ApprovalPending, before.UnixMilli()); err != nil {
		return fmt.Errorf("store: prune mode approvals: %w", err)
	}
	return nil
}
