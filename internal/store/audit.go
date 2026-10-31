package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Origin says where an audited action came from.
type Origin string

const OriginLocal Origin = "local"

// AuditEntry is one audit_log row. Params are hashed, never stored, so the
// log cannot leak secrets passed to an action.
type AuditEntry struct {
	Time   time.Time
	Origin Origin
	UID    int
	Action string
	Target string
	Params any
	Result string
}

// Audit appends e to the audit log. A zero Time means now.
func (db *DB) Audit(ctx context.Context, e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	hash, err := ParamsHash(e.Params)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO audit_log (ts, origin, uid, action, target, params_hash, result)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), string(e.Origin), e.UID, e.Action, e.Target, hash, e.Result)
	if err != nil {
		return fmt.Errorf("store: audit %s %s: %w", e.Action, e.Target, err)
	}
	return nil
}

// ParamsHash returns the hex SHA-256 of params encoded as JSON. Map keys are
// sorted by encoding/json, so equal params hash equally.
func ParamsHash(params any) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("store: hash params: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
