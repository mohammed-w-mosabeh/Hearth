package broker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Gate 10: whatever a mode file or hearthd sends, stack.batch passes only
// existing stacks and the listed verbs.
func FuzzStackBatchParams(f *testing.F) {
	f.Add(`{"items":[{"name":"web","verb":"stop"}],"interactive":false}`)
	f.Add(`{"items":[{"name":"web","verb":"start"},{"name":"ghost","verb":"stop"}]}`)
	f.Add(`{"items":[{"name":"../web","verb":"stop"}]}`)
	f.Add(`{"items":[{"name":"web","verb":"stop","args":["--privileged"]}]}`)
	f.Add(`{"items":[{"name":"web","verb":"down"}],"as_root":true}`)
	f.Add(`{"items":[{"name":"web","verb":"stop"}]}{"items":[]}`)
	r := newLifeRig(f)
	if _, err := r.call(f, "stack.deploy", deployParams(lifeSpec)); err != nil {
		f.Fatal(err)
	}
	m := r.methods["stack.batch"]
	f.Fuzz(func(t *testing.T, raw string) {
		if m.Check == nil {
			t.Fatal("stack.batch has no check before polkit")
		}
		if err := m.Check(context.Background(), json.RawMessage(raw)); err != nil {
			return
		}
		var v StackBatchParams
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("accepted what does not decode: %q", raw)
		}
		if len(v.Items) == 0 || len(v.Items) > MaxStackBatch {
			t.Fatalf("accepted %d items", len(v.Items))
		}
		seen := map[string]bool{}
		for _, it := range v.Items {
			if it.Name != "web" || seen[it.Name] {
				t.Fatalf("accepted stack %q: %q", it.Name, raw)
			}
			seen[it.Name] = true
			if _, ok := controlVerbs[it.Verb]; !ok {
				t.Fatalf("accepted verb %q", it.Verb)
			}
		}
	})
}

// The grants file is root's, but read as untrusted: a damaged one allows
// nothing, and restart is allowed unattended only by a grant that lists
// restart and start (v1.8, S15b).
func FuzzGrantsFile(f *testing.F) {
	f.Add(`[{"name":"web","verbs":["stop"],"uid":1000,"granted":1,"until":9999999999999}]`, "web", "stop", uint32(1000))
	f.Add(`[{"name":"web","verbs":["stop","restart"],"uid":1000,"until":9999999999999}]`, "web", "restart", uint32(1000))
	f.Add(`[{"name":"web","verbs":["stop","start","restart"],"uid":1000,"until":9999999999999}]`, "web", "restart", uint32(1000))
	f.Add(`[{"name":"web","verbs":["stop"],"uid":1000,"until":1}]`, "web", "stop", uint32(1000))
	f.Add(`{"web/1000":{}}`, "web", "stop", uint32(0))
	dir := filepath.Join(f.TempDir(), "stacks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.Fatal(err)
	}
	g := &grantStore{store: StackStore{Dir: dir}}
	now := time.UnixMilli(1793359297763)
	f.Fuzz(func(t *testing.T, file, name, verb string, uid uint32) {
		if err := os.WriteFile(filepath.Join(dir, fileGrants), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		if !g.allows(uid, []StackBatchItem{{Name: name, Verb: verb}}, now) {
			return
		}
		if verb != "stop" && verb != "start" && verb != "restart" {
			t.Fatalf("allowed %q unattended", verb)
		}
		var list []StackGrant
		if err := json.Unmarshal([]byte(file), &list); err != nil {
			t.Fatalf("a damaged file allowed %s %s", verb, name)
		}
		for _, gr := range list {
			if gr.Name == name && gr.UID == uid && now.UnixMilli() < gr.Until && contains(gr.Verbs, verb) {
				if verb == "restart" && !contains(gr.Verbs, "start") {
					t.Fatalf("allowed restart without start: %s", file)
				}
				return
			}
		}
		t.Fatalf("allowed %s %s for %d with no live grant: %s", verb, name, uid, file)
	})
}
