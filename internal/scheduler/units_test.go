package scheduler

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSyntax(t *testing.T) {
	for _, ok := range []string{"daily", "Mon..Fri 09:30", "*-*-01 00:00:00", "Sat,Sun *-*-* 10:00", "*:0/15", "2026-12-24 18:00 Europe/Berlin", "hourly", "*-*~03"} {
		if err := CalendarSyntax(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " daily", "daily ", "a  b", "-daily", "daily\nExecStart=/x", "daily%n", "$HOME", "daily;x", "daily\x00", strings.Repeat("a", 201), "daily\"", "--help"} {
		if CalendarSyntax(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"5min", "1h 30min", "2d", "90s", "1 h", "10 minutes"} {
		if err := SpanSyntax(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "5", "min", "-5min", "5min\nX=1", "5 parsecs", " 5min", "1h 1h 1h 1h 1h 1h 1h"} {
		if SpanSyntax(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Job{Name: "Backup photos", Script: "#!/bin/sh\nrsync -a ~/Pictures /mnt/backup\n", Timeout: 3600,
		Schedule: Schedule{Kind: KindCalendar, Value: "daily"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Job){
		"id":        func(j *Job) { j.ID = "../x" },
		"name":      func(j *Job) { j.Name = "" },
		"newline":   func(j *Job) { j.Name = "a\nb" },
		"long":      func(j *Job) { j.Name = strings.Repeat("é", MaxName+1) },
		"padded":    func(j *Job) { j.Name = " a" },
		"backslash": func(j *Job) { j.Name = `a\` },
		"shebang":   func(j *Job) { j.Script = "rm -rf ~" },
		"nul":       func(j *Job) { j.Script = "#!/bin/sh\x00" },
		"big":       func(j *Job) { j.Script = "#!/bin/sh\n" + strings.Repeat("x", MaxScript) },
		"timeout":   func(j *Job) { j.Timeout = 0 },
		"timeout2":  func(j *Job) { j.Timeout = MaxTimeout + 1 },
		"kind":      func(j *Job) { j.Schedule.Kind = "cron" },
		"span":      func(j *Job) { j.Schedule = Schedule{Kind: KindBoot, Value: "soon"} },
	}
	for name, f := range bad {
		j := good
		f(&j)
		if j.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestUnitFiles(t *testing.T) {
	j := Job{ID: "0a1b2c3d", Name: `50% "done" \ ok`, Timeout: 60, Notify: true, Schedule: Schedule{Kind: KindCalendar, Value: "Mon..Fri 09:30"}}
	svc := ServiceFile(j, `/home/me/My $files %h/"x"/run`)
	for _, want := range []string{
		"Description=50%% \"done\" \\ ok\n",
		"X-Hearth-Job=0a1b2c3d\n", "X-Hearth-Notify=yes\n", "Type=oneshot\n", "TimeoutStartSec=60s\n",
		`ExecStart="/home/me/My $files %%h/\"x\"/run"` + "\n",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	tm := TimerFile(j)
	for _, want := range []string{"OnCalendar=Mon..Fri 09:30\n", "Persistent=true\n", "Unit=hearth-job-0a1b2c3d.service\n", "WantedBy=timers.target\n"} {
		if !strings.Contains(tm, want) {
			t.Errorf("timer lacks %q:\n%s", want, tm)
		}
	}
	back, ok := FromUnits(j.ID, svc, tm)
	j.Script = ""
	if !ok || !reflect.DeepEqual(back, j) {
		t.Errorf("round trip:\n%+v\n%+v", back, j)
	}
	iv := TimerFile(Job{ID: "0a1b2c3d", Name: "x", Schedule: Schedule{Kind: KindInterval, Value: "15min"}})
	if !strings.Contains(iv, "OnActiveSec=15min\nOnUnitActiveSec=15min\n") || strings.Contains(iv, "Persistent") {
		t.Errorf("interval:\n%s", iv)
	}
	if _, ok := FromUnits("ffffffff", svc, tm); ok {
		t.Error("another job's units accepted")
	}
}

func keys(m map[string]map[string]string) []string {
	var out []string
	for s, kv := range m {
		for k := range kv {
			out = append(out, s+"."+k)
		}
	}
	sort.Strings(out)
	return out
}

// FuzzUnitFiles: whatever passes validation is written as exactly the
// expected keys (no injected line, key or section) and reads back the same.
func FuzzUnitFiles(f *testing.F) {
	f.Add("Backup", "daily", "calendar", "/home/me/.local/share/hearth/jobs/x/run", 60, true)
	f.Add("50% \\", "*:0/15", "calendar", "/a b/$c/%d", 1, false)
	f.Add("x\ny", "5min\nExecStart=/bin/x", "interval", "/r", 5, true)
	f.Add("a", "1h", "boot", "\"/r\n[Service]", 9, false)
	f.Fuzz(func(t *testing.T, name, sched, kind, run string, timeout int, notify bool) {
		j := Job{ID: "0a1b2c3d", Name: name, Script: "#!/bin/sh\n", Timeout: timeout, Notify: notify, Schedule: Schedule{Kind: kind, Value: sched}}
		if j.Validate() != nil || strings.ContainsAny(run, "\n\r\x00") {
			return
		}
		svc, tm := ServiceFile(j, run), TimerFile(j)
		ks := keys(Parse(svc))
		want := []string{"Service.ExecStart", "Service.TimeoutStartSec", "Service.Type", "Unit.Description", "Unit.X-Hearth-Job", "Unit.X-Hearth-Notify"}
		if !reflect.DeepEqual(ks, want) {
			t.Fatalf("service keys %v:\n%s", ks, svc)
		}
		tk := keys(Parse(tm))
		if len(tk) < 4 || len(tk) > 6 {
			t.Fatalf("timer keys %v:\n%s", tk, tm)
		}
		back, ok := FromUnits(j.ID, svc, tm)
		j.Script = ""
		if !ok || !reflect.DeepEqual(back, j) {
			t.Fatalf("round trip:\n%+v\n%+v", back, j)
		}
		if got := Parse(svc)["Service"]["ExecStart"]; !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Fatalf("ExecStart not quoted: %s", got)
		}
	})
}

func FuzzCalendarSyntax(f *testing.F) {
	for _, s := range []string{"daily", "Mon..Fri 09:30", "*:0/15", "-x", "a\nb"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if CalendarSyntax(s) != nil {
			return
		}
		if strings.ContainsAny(s, "\n\r\x00%$\"'\\;=[]") || strings.HasPrefix(s, "-") || len(s) > 200 {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzSpanSyntax(f *testing.F) {
	for _, s := range []string{"5min", "1h 30min", "x", "5min\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if SpanSyntax(s) != nil {
			return
		}
		if strings.ContainsAny(s, "\n\r\x00%$\"'\\;=[]-") || len(s) > 80 {
			t.Fatalf("accepted %q", s)
		}
	})
}
