package stacks

import (
	"context"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type nopAudit struct{}

func (nopAudit) Audit(context.Context, store.AuditEntry) error { return nil }

func TestCheckUpdates(t *testing.T) {
	_, fb := rig(t, fakeTags{})
	notes := &UpdateNotes{}
	reg := actions.NewRegistry(nopAudit{})
	Register(reg, fb, fakeTags{status: docker.UpdateNewer, remote: "sha256:" + strings.Repeat("b", 64)}, notes)
	if reg.AuthorityOf(Kind+"/check-updates", actions.Request{}) != actions.AuthorityNone {
		t.Fatal("check-updates never asks")
	}
	for _, r := range []actions.Request{{Target: "../x"}, {Target: "all", Params: map[string]string{"a": "b"}}} {
		if _, _, err := reg.PlanFor(context.Background(), Kind+"/check-updates", r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	// The preview asks nothing.
	if _, _, err := reg.PlanFor(context.Background(), Kind+"/check-updates", actions.Request{Target: "all"}); err != nil || len(fb.calls) != 0 {
		t.Fatalf("%v %+v", err, fb.calls)
	}
	u, err := reg.RunUnattended(context.Background(), Kind+"/check-updates", actions.Request{Target: "all"}, "automation:1")
	if err != nil || u.Result.Done != 1 || len(u.Result.Failed) != 0 {
		t.Fatalf("%v %+v", err, u)
	}
	for _, c := range fb.calls {
		if c.method != "stack.status" && c.method != "docker.images.inspect" {
			t.Fatalf("a check changed something: %s", c.method)
		}
	}
	if got := notes.Pending(context.Background(), fb.status[0]); len(got) != 1 || got[0] != "app" {
		t.Fatalf("the note is kept for the trigger: %v", got)
	}
	u, err = reg.RunUnattended(context.Background(), Kind+"/check-updates", actions.Request{Target: "gone"}, "automation:1")
	if err != nil || u.Result.Done != 0 || len(u.Result.Blocked) != 1 {
		t.Fatalf("%v %+v", err, u)
	}
}
