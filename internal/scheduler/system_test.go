package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func brokerMethods(t *testing.T) (map[string]broker.Method, BrokerConfig, *fakeSystemd) {
	t.Helper()
	dir := t.TempDir()
	fake := newFake()
	c := BrokerConfig{UnitDir: filepath.Join(dir, "etc"), JobsDir: filepath.Join(dir, "lib/jobs"),
		Bus: func(context.Context) (Bus, error) { return fake, nil },
		Run: func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
			if err := safeexec.Check(name, args...); err != nil {
				return safeexec.Result{}, err
			}
			if args[len(args)-1] == "bogus" {
				return safeexec.Result{Stderr: []byte("Failed to parse")}, errors.New("exit 1")
			}
			return safeexec.Result{Stdout: []byte("Normalized form: x\n")}, nil
		}}
	if err := os.MkdirAll(c.UnitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	m := map[string]broker.Method{}
	for _, x := range BrokerMethods(c) {
		if !x.Privileged || x.Action != ActionJobs || x.Check == nil {
			t.Errorf("%s: privileged %v action %q check %v", x.Name, x.Privileged, x.Action, x.Check != nil)
		}
		m[x.Name] = x
	}
	return m, c, fake
}

func call(m broker.Method, params any) (any, error) {
	b, _ := json.Marshal(params)
	if err := m.Check(context.Background(), b); err != nil {
		return nil, err
	}
	return m.Handle(context.Background(), broker.Caller{}, b)
}

func code(err error) int {
	var be *broker.Error
	if errors.As(err, &be) {
		return be.Code
	}
	return 0
}

func TestBrokerMethods(t *testing.T) {
	m, c, fake := brokerMethods(t)
	j := Job{ID: "0a1b2c3d", Name: "Nightly root", Script: "#!/bin/sh\napt-get clean\n", Timeout: 60,
		Schedule: Schedule{Kind: KindCalendar, Value: "daily"}}
	if _, err := call(m["sched.save"], SaveParams{Job: j, Create: true}); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(c.JobsDir, j.ID, "run")
	for p, mode := range map[string]os.FileMode{run: 0o755, filepath.Dir(run): 0o755,
		filepath.Join(c.UnitDir, "hearth-job-0a1b2c3d.service"): 0o644, filepath.Join(c.UnitDir, "hearth-job-0a1b2c3d.timer"): 0o644} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %o", p, fi.Mode(), err, mode)
		}
	}
	if !strings.Contains(fake.log(), "RestartUnit hearth-job-0a1b2c3d.timer") {
		t.Errorf("timer not started:\n%s", fake.log())
	}
	if out, err := call(m["sched.script"], IDParams{ID: j.ID}); err != nil || out.(map[string]string)["script"] != j.Script {
		t.Errorf("script %v %v", out, err)
	}
	for name, p := range map[string]any{
		"sched.save twice":    SaveParams{Job: j, Create: true},
		"sched.save no id":    SaveParams{Job: Job{Name: "x", Script: "#!/bin/sh", Timeout: 1, Schedule: Schedule{KindBoot, "5min"}}, Create: true},
		"sched.save bad":      SaveParams{Job: Job{ID: "0a1b2c3e", Name: "x\ny", Script: "#!/bin/sh", Timeout: 1, Schedule: Schedule{KindBoot, "5min"}}, Create: true},
		"sched.save meaning":  SaveParams{Job: Job{ID: "0a1b2c3e", Name: "x", Script: "#!/bin/sh", Timeout: 1, Schedule: Schedule{KindCalendar, "bogus"}}, Create: true},
		"sched.run traversal": IDParams{ID: "../../x"},
		"sched.run extra":     map[string]any{"id": j.ID, "cmd": "/bin/sh"},
	} {
		method := strings.Fields(name)[0]
		if _, err := call(m[method], p); code(err) != broker.CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, method := range []string{"sched.run", "sched.delete", "sched.script", "sched.pause"} {
		if _, err := call(m[method], IDParams{ID: "ffffffff"}); code(err) != broker.CodeNotFound {
			t.Errorf("%s of an unknown job: %v", method, err)
		}
	}
	if _, err := call(m["sched.save"], SaveParams{Job: Job{ID: "ffffffff", Name: "x", Script: "#!/bin/sh", Timeout: 1, Schedule: Schedule{KindBoot, "5min"}}}); code(err) != broker.CodeNotFound {
		t.Errorf("update of an unknown job: %v", err)
	}
	if _, err := call(m["sched.run"], IDParams{ID: j.ID}); err != nil || !strings.Contains(fake.log(), "StartUnit hearth-job-0a1b2c3d.service") {
		t.Errorf("run %v", err)
	}
	if _, err := call(m["sched.delete"], IDParams{ID: j.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(run)); !errors.Is(err, os.ErrNotExist) {
		t.Error("script left behind")
	}
}

type fakeCaller struct {
	method string
	params any
	err    error
}

func (f *fakeCaller) Call(_ context.Context, method string, params, out any) error {
	f.method, f.params = method, params
	if f.err == nil && out != nil {
		return json.Unmarshal([]byte(`{"script":"#!/bin/sh\n"}`), out)
	}
	return f.err
}

func TestSystemWriter(t *testing.T) {
	f := &fakeCaller{}
	w := &SystemWriter{B: f, Exists: func(id string) bool { return id == "0a1b2c3d" }}
	ctx := context.Background()
	_ = w.Save(ctx, Job{ID: "0a1b2c3d"}, false)
	if f.method != "sched.save" || f.params.(SaveParams).Create {
		t.Errorf("update: %s %+v", f.method, f.params)
	}
	_ = w.Save(ctx, Job{ID: "deadbeef"}, false)
	if !f.params.(SaveParams).Create {
		t.Error("create not marked")
	}
	if s, err := w.Script(ctx, "0a1b2c3d"); err != nil || s != "#!/bin/sh\n" {
		t.Errorf("script %q %v", s, err)
	}
	for err, want := range map[error]error{broker.ErrUnauthorized: ErrUnauthorized, broker.Errorf(broker.CodeNotFound, "x"): ErrNotFound} {
		f.err = err
		if got := w.Run(ctx, "0a1b2c3d"); !errors.Is(got, want) {
			t.Errorf("%v -> %v", err, got)
		}
	}
	f.err = broker.ErrNotMember
	var p Problem
	if err := w.Delete(ctx, "0a1b2c3d"); !errors.As(err, &p) || !strings.Contains(p.Msg, "hearth group") {
		t.Errorf("not member: %v", err)
	}
}

func TestSplitFailures(t *testing.T) {
	s, _, _ := userStore(t)
	ctx := context.Background()
	loud, err := s.Save(ctx, sample) // notify: true
	if err != nil {
		t.Fatal(err)
	}
	quiet := sample
	quiet.Name, quiet.Notify = "Quiet", false
	q, err := s.Save(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	units, jobs := SplitFailures(map[string]float64{
		"system/cups.service":                       1,
		"user/hearth-job-" + loud.ID + ".service":   1,
		"user/hearth-job-" + q.ID + ".service":      1,
		"system/hearth-job-0a1b2c3d.service":        1, // no system store here
		"user/hearth-job-deadbeef.service":          1, // gone
		"user/hearth-job-" + loud.ID + "-x.service": 1, // not a job unit name
	}, map[string]*Store{ScopeUser: s})
	if len(units) != 2 || units["system/cups.service"] != 1 || units["user/hearth-job-"+loud.ID+"-x.service"] != 1 {
		t.Errorf("units %v", units)
	}
	if len(jobs) != 1 || jobs["Backup"] != 1 {
		t.Errorf("jobs %v", jobs)
	}
}
