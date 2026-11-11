package posture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

func TestDebsecanCheckAction(t *testing.T) {
	root := t.TempDir()
	settings := &memSettings{m: map[string]json.RawMessage{}}
	s := scanner(t, root, settings)
	reg := actions.NewRegistry(&audit{})
	s.RegisterActions(reg)
	if reg.AuthorityOf("debsecan/check", actions.Request{}) != actions.AuthorityNone {
		t.Fatal("debsecan/check never asks")
	}
	if _, _, err := reg.PlanFor(context.Background(), "debsecan/check", actions.Request{Target: "x"}); err == nil {
		t.Fatal("target must be debsecan")
	}
	u, err := reg.RunUnattended(context.Background(), "debsecan/check", actions.Request{Target: "debsecan"}, "automation:1")
	if err != nil || u.Result.Done != 1 {
		t.Fatalf("%v %+v", err, u)
	}
	if v := s.CVEs(context.Background()); len(v.List) != 2 {
		t.Fatalf("the list is kept: %+v", v)
	}
	if err := os.Remove(filepath.Join(root, "usr/bin/debsecan")); err != nil {
		t.Fatal(err)
	}
	u, err = reg.RunUnattended(context.Background(), "debsecan/check", actions.Request{Target: "debsecan"}, "automation:1")
	if err != nil || u.Result.Done != 0 || len(u.Result.Failed) != 1 {
		t.Fatalf("%v %+v", err, u)
	}
}
