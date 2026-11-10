package store

import (
	"context"
	"testing"
)

// Automations audit as "automation:<id>"; nothing else new is accepted,
// and the table stays append-only after its rebuild (0025).
func TestAuditAutomationOrigin(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	for origin, ok := range map[Origin]bool{
		OriginLocal: true, "automation:12": true, "automation:1": true,
		"automation:": false, "automation:0": false, "automation:1x": false, "automation:-1": false, "file": false,
	} {
		err := db.Audit(ctx, AuditEntry{Origin: origin, Action: "a.b", Target: "t", Result: "ok"})
		if (err == nil) != ok {
			t.Errorf("%q: %v", origin, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_log SET result = 'x'`); err == nil {
		t.Fatal("update allowed")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_log`); err == nil {
		t.Fatal("delete allowed")
	}
}
