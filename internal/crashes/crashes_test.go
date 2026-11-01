package crashes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

var (
	web  = strings.Repeat("a", 64)
	when = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	us   = `"__REALTIME_TIMESTAMP":"1793354400000000"`
)

func names(id string) string {
	if id == web {
		return "web"
	}
	return ""
}

func TestClassify(t *testing.T) {
	other := strings.Repeat("b", 64)
	for cg, want := range map[string]*Owner{
		"/system.slice/cups.service":                     {OwnerUnit, "cups.service"},
		"/system.slice/docker-" + web + ".scope":         {OwnerContainer, "web"},
		"/system.slice/docker-" + web + ".scope/app/sub": {OwnerContainer, "web"},
		"/docker/" + web:                                 {OwnerContainer, "web"},
		"/system.slice/docker-" + other + ".scope":       {OwnerContainer, other[:12]},
		"/user.slice/user-1000.slice/user@1000.service/app.slice/app-hearth-firefox@1.service": {OwnerUnit, "app-hearth-firefox@1.service"},
		"/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-firefox-1234.scope": {OwnerApp, "gnome-firefox-1234"},
		"/user.slice/user-1000.slice/session-3.scope":                                          nil,
		"/user.slice/user-1000.slice/user@1000.service":                                        nil,
		"": nil, "/": nil,
	} {
		got := classify(cg, names)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%q: %+v, want %+v", cg, got, want)
		}
	}
}

func TestParseCrashes(t *testing.T) {
	out := `{` + us + `,"COREDUMP_COMM":"crasher","COREDUMP_EXE":"/usr/bin/crasher","COREDUMP_SIGNAL_NAME":"SIGSEGV","COREDUMP_UNIT":"crasher.service","COREDUMP_CGROUP":"/system.slice/crasher.service"}
{` + us + `,"COREDUMP_COMM":"inbox","COREDUMP_USER_UNIT":"mail.service","COREDUMP_UNIT":"user@1000.service","COREDUMP_CGROUP":"/user.slice/user-1000.slice/user@1000.service/app.slice/mail.service"}
{` + us + `,"COREDUMP_COMM":"nginx","COREDUMP_UNIT":"docker.service","COREDUMP_CGROUP":"/system.slice/docker-` + web + `.scope"}
{` + us + `,"COREDUMP_COMM":[1,2,3],"COREDUMP_EXE":"/opt/bin/raw"}
{` + us + `,"COREDUMP_SIGNAL_NAME":"SIGABRT"}
{"COREDUMP_COMM":"no time"}
not json

`
	got := parseCrashes([]byte(out), names)
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	if e := got[0]; e.Name != "crasher" || e.Signal != "SIGSEGV" || e.Exe != "/usr/bin/crasher" || *e.Owner != (Owner{OwnerUnit, "crasher.service"}) || e.Time != 1793354400000 {
		t.Fatalf("%+v", e)
	}
	if o := got[1].Owner; o == nil || o.Name != "mail.service" {
		t.Fatalf("a user unit, not the user manager: %+v", o)
	}
	if o := got[2].Owner; o == nil || *o != (Owner{OwnerContainer, "web"}) {
		t.Fatalf("the container, not docker.service: %+v", o)
	}
	if got[3].Name != "/opt/bin/raw" {
		t.Fatalf("%+v", got[3])
	}
}

func TestParseOOM(t *testing.T) {
	out := `{` + us + `,"MESSAGE":"oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,oom_memcg=/system.slice/docker-` + web + `.scope,task_memcg=/system.slice/docker-` + web + `.scope,task=java,pid=44,uid=0"}
{` + us + `,"MESSAGE":"oom-kill:constraint=CONSTRAINT_NONE,global_oom,task_memcg=/system.slice/cups.service,task=cupsd,pid=9,uid=0"}
{` + us + `,"MESSAGE":"oom-kill:constraint=CONSTRAINT_NONE,global_oom"}
{` + us + `,"MESSAGE":"something else,task=nope"}
`
	got := parseOOM([]byte(out), names)
	if len(got) != 2 || got[0].Name != "java" || got[0].Kind != KindOOM || *got[0].Owner != (Owner{OwnerContainer, "web"}) || got[1].Owner.Name != "cups.service" {
		t.Fatalf("%+v", got)
	}
}

func TestByDay(t *testing.T) {
	loc := time.FixedZone("x", 3*3600)
	at := func(d time.Duration) int64 { return when.Add(d).UnixMilli() }
	got := byDay([]Event{{Time: at(12 * time.Hour), Kind: KindCrash}, {Time: at(0), Kind: KindOOM}, {Time: at(time.Hour), Kind: KindCrash}}, loc)
	if len(got) != 2 || got[0] != (Day{"2026-10-30", 1, 1}) || got[1] != (Day{"2026-10-31", 1, 0}) {
		t.Fatalf("%+v", got)
	}
}

func TestClean(t *testing.T) {
	if got := clean("a\x00b\nc\x1b[31m"); got != "abc[31m" {
		t.Fatalf("%q", got)
	}
	if got := clean(strings.Repeat("é", 400)); len(got) > maxText || !utf8.ValidString(got) {
		t.Fatalf("%d", len(got))
	}
}

func run(crash, oom string, err error) Runner {
	return func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if cerr := safeexec.Check(name, args...); cerr != nil {
			return safeexec.Result{}, cerr
		}
		if name == "journalctl-crashes" {
			if crash == "" {
				return safeexec.Result{ExitCode: 1}, errors.New("exit status 1")
			}
			return safeexec.Result{Stdout: []byte(crash)}, err
		}
		return safeexec.Result{Stdout: []byte(oom)}, err
	}
}

func TestReport(t *testing.T) {
	core := filepath.Join(t.TempDir(), "core_pattern")
	if err := os.WriteFile(core, []byte("|/usr/lib/systemd/systemd-coredump %P\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	crash := `{"__REALTIME_TIMESTAMP":"1793354400000000","COREDUMP_COMM":"old"}` + "\n"
	oom := `{"__REALTIME_TIMESTAMP":"1793354500000000","MESSAGE":"oom-kill:constraint=x,task=new,pid=1"}` + "\n"
	a := New(Config{Run: run(crash, oom, nil), CorePattern: core, CanReadSystem: func() bool { return false }, Now: func() time.Time { return when }})
	r, err := a.Read(context.Background(), 30)
	if err != nil || len(r.Events) != 2 || r.Events[0].Name != "new" || !r.Limited || !r.Coredump || len(r.Days) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	none := New(Config{Run: run("", "", nil), CorePattern: filepath.Join(t.TempDir(), "missing")})
	r, err = none.Read(context.Background(), 7)
	if err != nil || len(r.Events) != 0 || r.Events == nil || r.Coredump {
		t.Fatalf("no match is not a failure: %+v %v", r, err)
	}
}

func TestHandle(t *testing.T) {
	a := New(Config{Run: run("x", "", errors.New("boom"))})
	for q, want := range map[string]int{"?days=1": 400, "?days=abc": 400, "?days=30": 502} {
		w := httptest.NewRecorder()
		a.Handle(w, httptest.NewRequest("GET", "/api/v1/crashes"+q, nil))
		if w.Code != want {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	w := httptest.NewRecorder()
	New(Config{Run: run("", "", nil)}).Handle(w, httptest.NewRequest("GET", "/api/v1/crashes", nil))
	var r Report
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// Secrets stay out: neither the message nor the command line is asked for.
func TestNoSecretFields(t *testing.T) {
	args := strings.Join(slices.Concat(safeexec.CrashArgs(1)), " ")
	for _, f := range []string{"CMDLINE", "ENVIRON", ",MESSAGE", "=MESSAGE,"} {
		if strings.Contains(args, f) {
			t.Fatalf("asks for %s: %s", f, args)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"__REALTIME_TIMESTAMP":"1","COREDUMP_COMM":"x","COREDUMP_CGROUP":"/a/b.service"}`))
	f.Add([]byte(`{"__REALTIME_TIMESTAMP":"1","MESSAGE":"oom-kill:task=x,task_memcg=/docker/` + strings.Repeat("a", 64) + `"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, e := range append(parseCrashes(b, names), parseOOM(b, names)...) {
			if e.Name == "" || len(e.Name) > maxText || !utf8.ValidString(e.Name) || strings.ContainsAny(e.Name, "\x00\n") {
				t.Fatalf("%+v", e)
			}
		}
	})
}

func FuzzClassify(f *testing.F) {
	f.Add("/system.slice/docker-" + web + ".scope")
	f.Add("/user.slice/app-x.scope/y.service")
	f.Fuzz(func(t *testing.T, s string) {
		if o := classify(s, nil); o != nil && (len(o.Name) > maxText || strings.ContainsAny(o.Name, "\x00\n")) {
			t.Fatalf("%+v", o)
		}
	})
}
