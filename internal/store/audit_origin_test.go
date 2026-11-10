package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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

// 0025 rebuilds audit_log: every row written before it survives as it was.
func TestAuditRebuildKeepsRows(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	d := &DB{DB: db}
	before := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrationFiles, ".")
	for _, e := range entries {
		if e.Name() < "0025" {
			b, _ := fs.ReadFile(migrationFiles, e.Name())
			before[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err := d.migrate(ctx, before); err != nil {
		t.Fatal(err)
	}
	for i, action := range []string{"service.restart", "mode.arm", "hearth.import"} {
		if err := d.Audit(ctx, AuditEntry{Time: time.UnixMilli(int64(1000 + i)), Origin: OriginLocal, UID: 1000, Action: action, Target: "t", Params: map[string]string{"n": action}, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	dump := func() string {
		rows, err := d.QueryContext(ctx, `SELECT id, ts, origin, uid, action, target, params_hash, result FROM audit_log ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var id, ts, uid int64
			var origin, action, target, hash, result string
			if err := rows.Scan(&id, &ts, &origin, &uid, &action, &target, &hash, &result); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(id, ts, origin, uid, action, target, hash, result))
		}
		return strings.Join(out, "\n")
	}
	was := dump()
	if err := d.migrate(ctx, migrationFiles); err != nil {
		t.Fatal(err)
	}
	if now := dump(); now != was || strings.Count(now, "\n") != 2 {
		t.Fatalf("rows changed:\n%s\nwas:\n%s", now, was)
	}
}
