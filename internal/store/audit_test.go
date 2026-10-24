package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAuditAppendOnly(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000)
	params := map[string]any{"password": "hunter2", "force": true}

	if err := db.Audit(ctx, AuditEntry{
		Time: at, Origin: OriginLocal, UID: 1000,
		Action: "service.restart", Target: "service:systemd:ssh.service",
		Params: params, Result: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	var ts int64
	var origin, action, target, hash, result string
	var uid int
	if err := db.QueryRowContext(ctx,
		`SELECT ts, origin, uid, action, target, params_hash, result FROM audit_log`).
		Scan(&ts, &origin, &uid, &action, &target, &hash, &result); err != nil {
		t.Fatal(err)
	}
	want, _ := ParamsHash(params)
	if ts != at.UnixMilli() || origin != "local" || uid != 1000 || action != "service.restart" ||
		target != "service:systemd:ssh.service" || hash != want || result != "ok" {
		t.Fatalf("row = %d %s %d %s %s %s %s", ts, origin, uid, action, target, hash, result)
	}

	for _, q := range []string{`UPDATE audit_log SET result = 'tampered'`, `DELETE FROM audit_log`} {
		_, err := db.ExecContext(ctx, q)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: got %v, want append-only error", q, err)
		}
	}
}

func TestAuditRejectsUnknownOrigin(t *testing.T) {
	db, _ := openTemp(t)
	err := db.Audit(context.Background(), AuditEntry{Origin: "evil", Action: "a", Target: "t", Result: "ok"})
	if err == nil {
		t.Fatal("accepted unknown origin")
	}
}

func TestParamsHashStableAndOpaque(t *testing.T) {
	a, _ := ParamsHash(map[string]any{"b": 1, "a": "secret"})
	b, _ := ParamsHash(map[string]any{"a": "secret", "b": 1})
	c, _ := ParamsHash(map[string]any{"a": "other", "b": 1})
	if a != b || a == c || len(a) != 64 || strings.Contains(a, "secret") {
		t.Fatalf("a=%s b=%s c=%s", a, b, c)
	}
	if _, err := ParamsHash(make(chan int)); err == nil {
		t.Fatal("hashed an unencodable value")
	}
}
