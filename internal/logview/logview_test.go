package logview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

type fakeBroker struct {
	lines []broker.LogLine
	err   error
	calls int
}

func (f *fakeBroker) Call(_ context.Context, method string, params, out any) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if method != "docker.containers.logs" {
		return errors.New("unexpected method " + method)
	}
	if p := params.(map[string]any); p["tail"] != readLines || p["timestamps"] != true {
		return fmt.Errorf("params %v", p)
	}
	*out.(*broker.Logs) = broker.Logs{Lines: f.lines}
	return nil
}

func noRun(context.Context, string, ...string) (safeexec.Result, error) {
	return safeexec.Result{}, errors.New("no journal here")
}

func TestValidSource(t *testing.T) {
	for _, s := range []string{"container:web", "system/cups.service", "user/app.service"} {
		if !ValidSource(s) {
			t.Errorf("refused %q", s)
		}
	}
	for _, s := range []string{"", "web", "container:", "container:../x", "other/cups.service", "system/", "system/a b", "system/-x.service", "system/x\n"} {
		if ValidSource(s) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestClean(t *testing.T) {
	if got := clean("\x1b[31mred\x1b[0m\tok\r\x00\n"); got != "red\tok" {
		t.Fatalf("%q", got)
	}
	long := clean(strings.Repeat("é", MaxLine))
	if len(long) > MaxLine || !utf8.ValidString(long) {
		t.Fatal(len(long))
	}
}

func TestLevels(t *testing.T) {
	for s, want := range map[string]string{
		"ERROR: boom": LevelError, "[warn] slow": LevelWarn, "level=debug x": LevelDebug, "all fine": LevelInfo,
		"terrible": LevelInfo, "panic: nil": LevelError, "connection failed": LevelError,
	} {
		if got := guessLevel(s); got != want {
			t.Errorf("%q: %s", s, got)
		}
	}
	for p, want := range map[int]string{0: LevelError, 3: LevelError, 4: LevelWarn, 5: LevelInfo, 6: LevelInfo, 7: LevelDebug} {
		if got := prioLevel(p); got != want {
			t.Errorf("%d: %s", p, got)
		}
	}
}

func TestFold(t *testing.T) {
	got := fold([]Line{{Time: 1, Text: "req 1 took 5ms"}, {Time: 2, Text: "req 2 took 7ms"}, {Time: 3, Text: "req 3 took 9ms"}, {Time: 4, Text: "other"}, {Time: 5, Text: "req 4 took 1ms"}})
	if len(got) != 3 || got[0].Count != 3 || got[0].Time != 3 || got[0].Text != "req 3 took 9ms" || got[1].Count != 0 || got[2].Count != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSearch(t *testing.T) {
	re, err := parseQuery([]string{"container:web"}, "bo+m")
	if err != nil {
		t.Fatal(err)
	}
	got := shape([]Line{{Text: "fine"}, {Text: "boooom"}, {Text: "fine again"}}, re)
	if len(got) != 1 || got[0].Text != "boooom" {
		t.Fatalf("%+v", got)
	}
}

func TestParseQuery(t *testing.T) {
	five := []string{"container:a", "container:b", "container:c", "container:d", "container:e"}
	for _, c := range []struct {
		s  []string
		re string
	}{
		{nil, ""}, {five, ""}, {[]string{"container:a", "container:a"}, ""}, {[]string{"nope"}, ""},
		{[]string{"container:a"}, "("}, {[]string{"container:a"}, strings.Repeat("a", 201)}, {[]string{"container:a"}, ".*"},
	} {
		if _, err := parseQuery(c.s, c.re); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

// Gate 6: a noisy container stays within the cap, however much it logs.
func TestNoisyContainerStaysInItsCap(t *testing.T) {
	lines := make([]broker.LogLine, 0, 20000)
	for i := range 20000 {
		// Every line differs, so nothing folds, and each is far over the limit.
		lines = append(lines, broker.LogLine{Stream: "stdout", Text: fmt.Sprintf("2026-11-01T10:00:00.000000000Z %c%s\n", 'a'+rune(i%26), strings.Repeat(string(rune('a'+(i/26)%26)), 8192))})
	}
	a := New(&fakeBroker{lines: lines}, noRun)
	s := a.read(context.Background(), "container:noisy", nil)
	if s.Error != "" || len(s.Lines) > MaxShown {
		t.Fatalf("%s, %d lines", s.Error, len(s.Lines))
	}
	size := 0
	for _, l := range s.Lines {
		if len(l.Text) > MaxLine {
			t.Fatalf("line of %d bytes", len(l.Text))
		}
		size += len(l.Text)
	}
	if size > MaxShown*MaxLine {
		t.Fatalf("%d bytes", size)
	}
	// What the reply keeps alive is its own text, not what was read.
	a.Broker = nil
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if max := uint64(MaxShown*MaxLine) * 4; m.HeapAlloc > max+(16<<20) {
		t.Fatalf("heap %d after the read", m.HeapAlloc)
	}
	runtime.KeepAlive(s)
}

func TestHandle(t *testing.T) {
	f := &fakeBroker{lines: []broker.LogLine{{Text: "2026-11-01T10:00:00Z <img src=x onerror=1>\n"}, {Text: "no stamp ERROR\n"}}}
	a := New(f, noRun)
	get := func(q url.Values) (int, string) {
		w := httptest.NewRecorder()
		a.Handle(w, httptest.NewRequest("GET", "/api/v1/logs?"+q.Encode(), nil))
		return w.Code, w.Body.String()
	}
	if code, _ := get(url.Values{}); code != 400 {
		t.Fatal(code)
	}
	if code, body := get(url.Values{"s": {"container:web"}, "re": {"("}}); code != 400 || !strings.Contains(body, "RE2") {
		t.Fatal(code, body)
	}
	if f.calls != 0 {
		t.Fatal("a refused query read logs")
	}
	code, body := get(url.Values{"s": {"container:web", "system/x.service"}})
	var r struct{ Sources []Source }
	if err := json.Unmarshal([]byte(body), &r); err != nil || code != 200 || len(r.Sources) != 2 {
		t.Fatal(code, body)
	}
	if c := r.Sources[0]; len(c.Lines) != 2 || c.Lines[0].Text != "<img src=x onerror=1>" || c.Lines[0].Time == 0 || c.Lines[1].Level != LevelError {
		t.Fatalf("%+v", c)
	}
	if r.Sources[1].Error == "" {
		t.Fatal("a failed source must say so, and the other still shows")
	}
}

func FuzzQuery(f *testing.F) {
	f.Add("container:web", "system/a.service", "bo+m")
	f.Add("../x", "user/", "(")
	f.Fuzz(func(t *testing.T, a, b, re string) {
		if _, err := parseQuery([]string{a, b}, re); err == nil && (!ValidSource(a) || !ValidSource(b) || a == b) {
			t.Fatalf("%q %q", a, b)
		}
	})
}

func FuzzClean(f *testing.F) {
	f.Add("\x1b[31mred\x1b[0m")
	f.Fuzz(func(t *testing.T, s string) {
		got := clean(s)
		if len(got) > MaxLine || !utf8.ValidString(got) || strings.ContainsAny(got, "\x00\n\r\x1b") {
			t.Fatalf("%q", got)
		}
	})
}
