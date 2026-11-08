package broker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStackGrants(t *testing.T) {
	now := time.UnixMilli(1793359297763)
	r := newLifeRigWith(t, func(_ *lifeRig, c *StackConfig) { c.Now = func() time.Time { return now } })
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	// Only a stack that exists, and nothing but its name and start.
	for name, p := range map[string]any{
		"missing":   map[string]any{"name": "ghost"},
		"bad name":  map[string]any{"name": "../x"},
		"restart":   map[string]any{"name": "web", "verbs": []string{"restart"}},
		"extra":     map[string]any{"name": "web", "until": 1},
		"bad start": map[string]any{"name": "web", "start": "yes"},
	} {
		if _, err := r.call(t, "stack.grant.set", p); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := r.methods["stack.grant.set"]
	if !m.Privileged || m.Action != ActionStackGrant {
		t.Fatal("stack.grant.set must ask polkit")
	}
	raw, _ := json.Marshal(map[string]any{"name": "web", "start": true})
	if a, d := m.Authorize(raw); a != ActionStackGrant || d["stack"] != "web" || d["allows"] != "stop or start" {
		t.Fatalf("%s %v", a, d)
	}
	res, err := r.methods["stack.grant.set"].Handle(context.Background(), Caller{UID: 1000}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if g := res.(StackGrant); g.Until != now.Add(GrantTTL).UnixMilli() || len(g.Verbs) != 2 || g.UID != 1000 {
		t.Fatalf("%+v", g)
	}
	// Root-owned, the caller's own, listed only to that caller.
	fi, err := os.Stat(filepath.Join(r.store.Dir, fileGrants))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	list := func(uid uint32) []StackGrant {
		t.Helper()
		out, err := r.methods["stack.grant.list"].Handle(context.Background(), Caller{UID: uid}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.([]StackGrant)
	}
	if l := list(1000); len(l) != 1 || l[0].Name != "web" {
		t.Fatalf("%+v", l)
	}
	if l := list(1001); len(l) != 0 {
		t.Fatalf("another user sees %+v", l)
	}
	if r.methods["stack.grant.list"].Privileged || r.methods["stack.grant.revoke"].Privileged {
		t.Fatal("listing and revoking need no password")
	}
	g := r.methods["stack.batch"]
	items := []StackBatchItem{{Name: "web", Verb: "stop"}}
	stop, _ := json.Marshal(StackBatchParams{Items: items})
	if decided, ok := g.Authorized(context.Background(), Caller{UID: 1001}, stop); !decided || ok {
		t.Fatal("another user's grant allowed a batch")
	}
	if decided, ok := g.Authorized(context.Background(), Caller{UID: 1000}, stop); !decided || !ok {
		t.Fatal("the grant did not allow its batch")
	}
	// Revoking takes it away at once, for that user only.
	rev, _ := json.Marshal(StackGrantName{Name: "web"})
	if _, err := r.methods["stack.grant.revoke"].Handle(context.Background(), Caller{UID: 1001}, rev); err != nil {
		t.Fatal(err)
	}
	if l := list(1000); len(l) != 1 {
		t.Fatal("another user revoked the grant")
	}
	if _, err := r.methods["stack.grant.revoke"].Handle(context.Background(), Caller{UID: 1000}, rev); err != nil {
		t.Fatal(err)
	}
	if decided, ok := g.Authorized(context.Background(), Caller{UID: 1000}, stop); !decided || ok {
		t.Fatal("a revoked grant still allowed a batch")
	}
	// Removing the stack removes its grants.
	if _, err := r.methods["stack.grant.set"].Handle(context.Background(), Caller{UID: 1000}, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(t, "stack.remove", map[string]any{"name": "web"}); err != nil {
		t.Fatal(err)
	}
	if l := list(1000); len(l) != 0 {
		t.Fatalf("%+v", l)
	}
	// A damaged or linked grants file allows nothing.
	if err := os.WriteFile(filepath.Join(r.store.Dir, fileGrants), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if decided, ok := g.Authorized(context.Background(), Caller{UID: 1000}, stop); !decided || ok {
		t.Fatal("a damaged file allowed a batch")
	}
	_ = os.Remove(filepath.Join(r.store.Dir, fileGrants))
	if err := os.Symlink("/etc/passwd", filepath.Join(r.store.Dir, fileGrants)); err != nil {
		t.Fatal(err)
	}
	if decided, ok := g.Authorized(context.Background(), Caller{UID: 1000}, stop); !decided || ok {
		t.Fatal("a linked file allowed a batch")
	}
}
