package reports

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

func TestReportGenerate(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	w := &Weekly{B: b, Settings: memSettings{}, Dir: filepath.Join(t.TempDir(), "reports"), Clock: b.cfg.Clock}
	reg := actions.NewRegistry(&audit{})
	w.RegisterActions(reg)
	if reg.AuthorityOf("report/generate", actions.Request{}) != actions.AuthorityNone {
		t.Fatal("report/generate never asks")
	}
	for _, r := range []actions.Request{{Target: "year"}, {Target: "day", Params: map[string]string{"x": "y"}}, {Target: "../day"}} {
		if _, _, err := reg.PlanFor(context.Background(), "report/generate", r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	for range 2 {
		u, err := reg.RunUnattended(context.Background(), "report/generate", actions.Request{Target: "day"}, "automation:1")
		if err != nil || u.Result.Done != 1 {
			t.Fatalf("%v %+v", err, u)
		}
	}
	saved := w.List()
	if len(saved) != 2 {
		t.Fatalf("a second report the same day keeps the first: %+v", saved)
	}
	for _, s := range saved {
		if _, ok := w.Read(s.Name); !ok {
			t.Errorf("%s is not served", s.Name)
		}
		fi, err := os.Stat(filepath.Join(w.Dir, s.Name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", s.Name, err, fi.Mode())
		}
	}
	if _, err := w.Generate(context.Background(), "year"); err == nil {
		t.Fatal("an unknown period must fail")
	}
}
