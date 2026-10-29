package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// fakeSystemd is a user manager: units are active or not, calls are logged.
type fakeSystemd struct {
	mu     sync.Mutex
	calls  []string
	active map[string]string // unit -> ActiveState
	timer  map[string]uint64 // unit -> NextElapseUSecRealtime
}

func newFake() *fakeSystemd {
	return &fakeSystemd{active: map[string]string{}, timer: map[string]uint64{}}
}

func (f *fakeSystemd) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := strings.TrimPrefix(method, sdManager+".")
	name := ""
	switch v := args; {
	case len(v) > 0:
		switch a := v[0].(type) {
		case string:
			name = a
		case []string:
			name = strings.Join(a, ",")
		}
	}
	f.calls = append(f.calls, m+" "+name)
	switch m {
	case "GetUnit", "LoadUnit":
		*out[0].(*dbus.ObjectPath) = dbus.ObjectPath("/unit/" + strings.NewReplacer(".", "_", "-", "_").Replace(name))
	case "RestartUnit", "StartUnit":
		f.active[name] = "active"
		if strings.HasSuffix(name, ".timer") {
			f.timer[name] = 1793200000000000
		}
	case "StopUnit":
		f.active[name] = "inactive"
	}
	return nil
}

func (f *fakeSystemd) GetAll(_ context.Context, _ string, p dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(string(p), "/unit/")
	unit := ""
	for _, suf := range []string{"_timer", "_service"} {
		if base, ok := strings.CutSuffix(name, suf); ok {
			unit = strings.Replace(strings.ReplaceAll(base, "_", "-"), "hearth-job-", "hearth-job-", 1) + "." + suf[1:]
		}
	}
	state := f.active[unit]
	if state == "" {
		state = "inactive"
	}
	switch iface {
	case "org.freedesktop.systemd1.Unit":
		return map[string]dbus.Variant{"ActiveState": dbus.MakeVariant(state)}, nil
	case "org.freedesktop.systemd1.Timer":
		return map[string]dbus.Variant{"NextElapseUSecRealtime": dbus.MakeVariant(f.timer[unit]), "LastTriggerUSec": dbus.MakeVariant(uint64(0))}, nil
	case "org.freedesktop.systemd1.Service":
		return map[string]dbus.Variant{"Result": dbus.MakeVariant("success"), "ExecMainStatus": dbus.MakeVariant(int32(0))}, nil
	}
	return nil, errors.New("no such interface")
}

func (f *fakeSystemd) Close() error { return nil }

func (f *fakeSystemd) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func userStore(t *testing.T) (*Store, *fakeSystemd, string) {
	t.Helper()
	dir := t.TempDir()
	fake := newFake()
	bus := func(context.Context) (Bus, error) { return fake, nil }
	units, jobs := filepath.Join(dir, "systemd/user"), filepath.Join(dir, "data/jobs")
	return &Store{Scope: ScopeUser, UnitDir: units, Bus: bus, W: &UserWriter{UnitDir: units, JobsDir: jobs, Bus: bus}}, fake, dir
}

var sample = Job{Name: "Backup", Script: "#!/bin/sh\necho hi\n", Timeout: 600, Notify: true,
	Schedule: Schedule{Kind: KindCalendar, Value: "daily"}}

func TestUserJobLifecycle(t *testing.T) {
	s, fake, dir := userStore(t)
	ctx := context.Background()
	j, err := s.Save(ctx, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(j.ID) {
		t.Fatalf("id %q", j.ID)
	}
	base := UnitBase(j.ID)
	run := filepath.Join(dir, "data/jobs", j.ID, "run")
	if fi, err := os.Stat(run); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("script %v %v", fi, err)
	}
	svc, _ := os.ReadFile(filepath.Join(s.UnitDir, base+".service")) // #nosec G304 -- the test's temp dir
	if !strings.Contains(string(svc), `ExecStart="`+run+`"`) {
		t.Errorf("service:\n%s", svc)
	}
	for _, want := range []string{"Reload ", "EnableUnitFiles " + base + ".timer", "RestartUnit " + base + ".timer"} {
		if !strings.Contains(fake.log(), want) {
			t.Errorf("no %q in\n%s", want, fake.log())
		}
	}
	l, err := s.List(ctx)
	if err != nil || len(l) != 1 || l[0].Name != "Backup" || l[0].Paused || l[0].Next != 1793200000000 || l[0].Scope != ScopeUser || l[0].Script != "" {
		t.Fatalf("list %+v %v", l, err)
	}
	got, err := s.Get(ctx, j.ID)
	if err != nil || got.Script != sample.Script || got.Timeout != 600 || !got.Notify {
		t.Fatalf("get %+v %v", got, err)
	}

	// Paused, then edited: it stays paused.
	if err := s.W.SetPaused(ctx, j.ID, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.log(), "DisableUnitFiles "+base+".timer") {
		t.Error("pause did not disable")
	}
	fake.calls = nil
	edit := got
	edit.Name, edit.Schedule = "Backup v2", Schedule{Kind: KindInterval, Value: "2h"}
	if _, err := s.Save(ctx, edit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.log(), "RestartUnit") {
		t.Errorf("a paused job was restarted:\n%s", fake.log())
	}
	if l, _ := s.List(ctx); !l[0].Paused || l[0].Name != "Backup v2" || l[0].Schedule.Value != "2h" {
		t.Errorf("after edit %+v", l[0])
	}
	if _, err := s.Save(ctx, Job{ID: "deadbeef", Name: "x", Script: "#!/bin/sh\n", Timeout: 1, Schedule: Schedule{Kind: KindBoot, Value: "5min"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of an unknown job: %v", err)
	}

	if err := s.W.Run(ctx, j.ID); err != nil || !strings.Contains(fake.log(), "StartUnit "+base+".service") {
		t.Errorf("run %v", err)
	}
	if err := s.W.Delete(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(s.UnitDir, base+".timer"), filepath.Join(s.UnitDir, base+".service"), run} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind", p)
		}
	}
	if l, _ := s.List(ctx); len(l) != 0 {
		t.Errorf("after delete %+v", l)
	}
}

func TestListIgnoresForeignUnits(t *testing.T) {
	s, _, _ := userStore(t)
	_ = os.MkdirAll(s.UnitDir, 0o700)
	// Not ours: no X-Hearth-Job, a wrong id, a bad name.
	_ = os.WriteFile(filepath.Join(s.UnitDir, "hearth-job-0a1b2c3d.timer"), []byte("[Timer]\nOnCalendar=daily\n"), 0o600)
	_ = os.WriteFile(filepath.Join(s.UnitDir, "hearth-job-0a1b2c3d.service"), []byte("[Unit]\nX-Hearth-Job=ffffffff\n"), 0o600)
	_ = os.WriteFile(filepath.Join(s.UnitDir, "hearth-job-../x.timer"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(s.UnitDir, "backup.timer"), nil, 0o600)
	if l, err := s.List(context.Background()); err != nil || len(l) != 0 {
		t.Errorf("%+v %v", l, err)
	}
}

func TestParseRuns(t *testing.T) {
	journal := strings.Join([]string{
		`{"__REALTIME_TIMESTAMP":"1000000000","USER_INVOCATION_ID":"a1","MESSAGE":"Starting x..."}`,
		`{"__REALTIME_TIMESTAMP":"1000500000","_SYSTEMD_INVOCATION_ID":"a1","MESSAGE":"hello"}`,
		`{"__REALTIME_TIMESTAMP":"1000600000","_SYSTEMD_INVOCATION_ID":"a1","MESSAGE":[104,105,255]}`,
		`{"__REALTIME_TIMESTAMP":"1000700000","USER_INVOCATION_ID":"a1","EXIT_STATUS":"3","MESSAGE":"Main process exited"}`,
		`{"__REALTIME_TIMESTAMP":"1000800000","USER_INVOCATION_ID":"a1","JOB_RESULT":"failed","MESSAGE":"Failed to start"}`,
		`{"__REALTIME_TIMESTAMP":"2000000000","INVOCATION_ID":"b2","MESSAGE":"Starting x..."}`,
		`{"__REALTIME_TIMESTAMP":"2000100000","INVOCATION_ID":"b2","JOB_RESULT":"done","MESSAGE":"Finished"}`,
		`{"__REALTIME_TIMESTAMP":"3000000000","USER_INVOCATION_ID":"c3","MESSAGE":"Starting x..."}`,
		`{"__REALTIME_TIMESTAMP":"3000000000","MESSAGE":"no invocation"}`,
		// Output that lost its invocation: the run that started last
		// before it, not one that started later or a manager message.
		`{"__REALTIME_TIMESTAMP":"1000550000","_TRANSPORT":"stdout","MESSAGE":"lost a1"}`,
		`{"__REALTIME_TIMESTAMP":"2000050000","_TRANSPORT":"stdout","MESSAGE":"lost b2"}`,
		`{"__REALTIME_TIMESTAMP":"2000060000","_TRANSPORT":"journal","MESSAGE":"not output"}`,
		`{"__REALTIME_TIMESTAMP":"500000000","_TRANSPORT":"stdout","MESSAGE":"before any run"}`,
		`not json`,
		`{"__REALTIME_TIMESTAMP":"3000100000","_SYSTEMD_INVOCATION_ID":"c3","MESSAGE":"partial`,
	}, "\n")
	runs := parseRuns([]byte(journal), true)
	if len(runs) != 3 {
		t.Fatalf("%+v", runs)
	}
	running, ok, failed := runs[0], runs[1], runs[2]
	if running.Result != "running" || running.End != 0 || running.Start != 3000000 {
		t.Errorf("running %+v", running)
	}
	if ok.Result != "success" || ok.Exit == nil || *ok.Exit != 0 || ok.End != 2000100 || len(ok.Output) != 1 || ok.Output[0] != "lost b2" {
		t.Errorf("ok %+v", ok)
	}
	if failed.Result != "failed" || failed.Exit == nil || *failed.Exit != 3 || len(failed.Output) != 3 ||
		failed.Output[0] != "hello" || failed.Output[1] != "hi�" || failed.Output[2] != "lost a1" || failed.End-failed.Start != 800 {
		t.Errorf("failed %+v", failed)
	}
}

func TestCheckSchedule(t *testing.T) {
	calendar := "  Original form: Mon..Fri 09:30\nNormalized form: Mon..Fri *-*-* 09:30:00\n    Next elapse: Thu 2026-10-29 09:30:00 EET\n       (in UTC): Thu 2026-10-29 07:30:00 UTC\n       From now: 21h left\n   Iteration #2: Fri 2026-10-30 09:30:00 EET\n       (in UTC): Fri 2026-10-30 07:30:00 UTC\n"
	var got []string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if err := safeexec.Check(name, args...); err != nil {
			return safeexec.Result{}, err
		}
		got = append(got, name+" "+strings.Join(args, " "))
		if args[len(args)-1] == "bogus" {
			return safeexec.Result{Stderr: []byte("Failed to parse calendar specification 'bogus': Invalid argument\n"), ExitCode: 1}, errors.New("exit 1")
		}
		if name == "timespan-check" {
			return safeexec.Result{Stdout: []byte("Original: 90min\n      μs: 5400000000\n   Human: 1h 30min\n")}, nil
		}
		return safeexec.Result{Stdout: []byte(calendar)}, nil
	}
	c := CheckSchedule(context.Background(), run, Schedule{Kind: KindCalendar, Value: "Mon..Fri 09:30"})
	if !c.Valid || c.Normalized != "Mon..Fri *-*-* 09:30:00" || len(c.Next) != 2 || c.Next[0] != 1793259000000 {
		t.Errorf("%+v", c)
	}
	if c := CheckSchedule(context.Background(), run, Schedule{Kind: KindInterval, Value: "90min"}); !c.Valid || c.Normalized != "1h 30min" {
		t.Errorf("%+v", c)
	}
	if c := CheckSchedule(context.Background(), run, Schedule{Kind: KindCalendar, Value: "bogus"}); c.Valid || !strings.Contains(c.Error, "Invalid argument") {
		t.Errorf("%+v", c)
	}
	n := len(got)
	for _, s := range []Schedule{{KindCalendar, "--help"}, {KindCalendar, "daily\nx"}, {KindBoot, "soon"}, {"cron", "* * * * *"}} {
		if c := CheckSchedule(context.Background(), run, s); c.Valid {
			t.Errorf("%+v accepted", s)
		}
	}
	if len(got) != n {
		t.Errorf("bad schedules reached systemd-analyze: %v", got[n:])
	}
}
