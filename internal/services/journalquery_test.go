package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func query(t *testing.T, q string) *httptest.ResponseRecorder {
	t.Helper()
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if name != "journalctl-query" {
			t.Errorf("%s", name)
		}
		return safeexec.Result{}, nil
	}
	rec := httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return true })(rec, httptest.NewRequest("GET", "/api/v1/journal?"+q, nil))
	return rec
}

func TestJournalQueryArgs(t *testing.T) {
	var got []string
	run := func(_ context.Context, _ string, args ...string) (safeexec.Result, error) {
		got = args
		return safeexec.Result{}, nil
	}
	q := url.Values{"lines": {"50"}, "priority": {"3"}, "since": {"1793300000"}, "until": {"1793400000"}, "boot": {strings.Repeat("a", 32)},
		"unit": {"user/app.service"}, "text": {" failed (exit.*) "}}
	rec := httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return false })(rec, httptest.NewRequest("GET", "/?"+q.Encode(), nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{"--lines=50", "--priority=3", "--since=@1793300000", "--until=@1793400000", "--boot=" + strings.Repeat("a", 32),
		"--user-unit=app.service", `--grep=failed \(exit\.\*\)`} {
		if !slices.Contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
	if !strings.Contains(rec.Body.String(), `"limited":true`) {
		t.Errorf("%s", rec.Body)
	}
	q.Del("since")
	q.Set("after", "s=ab;i=1")
	rec = httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return false })(rec, httptest.NewRequest("GET", "/?"+q.Encode(), nil))
	if rec.Code != 200 || !slices.Contains(got, "--after-cursor=s=ab;i=1") {
		t.Errorf("%d %v", rec.Code, got)
	}
}

func TestJournalQueryRefuses(t *testing.T) {
	for _, q := range []string{
		"lines=0", "lines=1001", "lines=x", "priority=8", "priority=-1", "since=yesterday", "since=-1", "until=@1", "boot=-1", "boot=abc",
		"unit=cups.service", "unit=other/x.service", "unit=system/--root=/", "after=--file=/etc/passwd", "after=x",
		"text=" + url.QueryEscape("a\nb"), "text=" + strings.Repeat("a", safeexec.MaxJournalGrep+1),
	} {
		if rec := query(t, q); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// Text that looks like a flag stays the value of --grep.
	if rec := query(t, "text=--file%3D/etc/passwd"); rec.Code != 200 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestSafeexecJournalQuery(t *testing.T) {
	base := append(append([]string{}, safeexec.JournalQueryFixedArgs...), "--lines=10")
	if err := safeexec.Check("journalctl-query", base...); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{
		{"--file=/etc/passwd"}, {"--directory=/tmp"}, {"--root=/"}, {"--flush"}, {"--rotate"}, {"--vacuum-size=1"}, {"--unit"},
		{"--merge=x"}, {"--priority=3", "--priority=4"}, {"--unit=a.service", "--user-unit=b.service"}, {"--grep=a\nb"}, {"--boot=-1"},
		{"--since=yesterday"}, {"--unit=--help"}, {"--since=@1", "--after-cursor=s=ab"},
	} {
		if safeexec.Check("journalctl-query", append(append([]string{}, base...), extra...)...) == nil {
			t.Errorf("%v accepted", extra)
		}
	}
	if safeexec.Check("journalctl-query", "--lines=10") == nil {
		t.Error("without the fixed arguments")
	}
}

// journalctl refuses --since with a cursor, so a filtered search must
// page without it and stop at the same time.
func TestJournalQueryPagesPastSince(t *testing.T) {
	var got []string
	line := func(i, sec int) string {
		return `{"__CURSOR":"s=` + strconv.Itoa(i) + `","__REALTIME_TIMESTAMP":"` + strconv.Itoa(sec) + `000000","MESSAGE":"m"}` + "\n"
	}
	out := line(1, 200) + line(2, 150) + line(3, 50)
	run := func(_ context.Context, _ string, args ...string) (safeexec.Result, error) {
		got = args
		if err := safeexec.Check("journalctl-query", args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		return safeexec.Result{Stdout: []byte(out)}, nil
	}
	rec := httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return true })(rec, httptest.NewRequest("GET", "/?lines=3&since=100&after=s%3D0", nil))
	var res JournalQuery
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(res.Entries) != 2 || res.Next != "" {
		t.Errorf("%+v", res)
	}
	if slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--since") }) {
		t.Errorf("%v", got)
	}
	// The first page lets journalctl cut it.
	rec = httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return true })(rec, httptest.NewRequest("GET", "/?lines=3&since=100", nil))
	if !slices.Contains(got, "--since=@100") {
		t.Errorf("%v", got)
	}
}

func TestJournalQueryPaging(t *testing.T) {
	line := func(i int) string {
		return `{"__CURSOR":"s=` + strconv.Itoa(i) + `","__REALTIME_TIMESTAMP":"1000000","MESSAGE":"m","PRIORITY":"3","_SYSTEMD_USER_UNIT":"a.service","_SYSTEMD_UNIT":"user@1000.service"}` + "\n"
	}
	out := line(1) + line(2)
	run := func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{Stdout: []byte(out)}, nil
	}
	get := func(q string) JournalQuery {
		rec := httptest.NewRecorder()
		HandleJournalQuery(run, func() bool { return true })(rec, httptest.NewRequest("GET", "/?"+q, nil))
		var res JournalQuery
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if r := get("lines=2"); r.Next != "s=2" || r.Entries[0].Unit != "a.service" || r.Entries[0].Prio != 3 {
		t.Errorf("%+v", r)
	}
	if r := get("lines=3"); r.Next != "" {
		t.Errorf("a short page is the end: %+v", r)
	}
}

func TestJournalQueryFailures(t *testing.T) {
	run := func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{}, context.DeadlineExceeded
	}
	rec := httptest.NewRecorder()
	HandleJournalQuery(run, func() bool { return true })(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 504 {
		t.Errorf("%d", rec.Code)
	}
	exit1 := func(stderr string) Runner {
		return func(context.Context, string, ...string) (safeexec.Result, error) {
			return safeexec.Result{ExitCode: 1, Stderr: []byte(stderr)}, errors.New("exited with 1")
		}
	}
	rec = httptest.NewRecorder()
	HandleJournalQuery(exit1(""), func() bool { return true })(rec, httptest.NewRequest("GET", "/?text=x", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("no match: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	HandleJournalQuery(exit1("Failed to open journal"), func() bool { return true })(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 502 {
		t.Errorf("a real failure: %d", rec.Code)
	}
}

func TestBoots(t *testing.T) {
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	got, err := parseBoots([]byte(`[{"index":-1,"boot_id":"` + a + `","first_entry":1000,"last_entry":2000},{"index":0,"boot_id":"` + b + `","first_entry":3000,"last_entry":4000},{"boot_id":"bad","first_entry":0}]`))
	if err != nil || len(got) != 2 || got[0].ID != b || got[0].First != 3 || got[1].ID != a {
		t.Fatalf("%v %+v", err, got)
	}
	run := func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{}, errors.New("no")
	}
	rec := httptest.NewRecorder()
	HandleBoots(run)(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"boots":[]`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
