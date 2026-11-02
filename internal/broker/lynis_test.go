package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const lynisSample = `# Lynis Report
report_version_major=1
lynis_version=3.1.4
hostname=secret-host
real_user[]=mohammed,1000
warning[]=AUTH-9286|Reported issue|-|-|
warning[]=../evil|<img src=x onerror=alert(1)>|/etc/x|Fix it|
suggestion[]=SSH-7408|Consider hardening SSH|AllowUsers:YES|a|b|
suggestion[]=KRNL-5820|` + "\x1b[31mNo\x00 core dump" + `|-|-|
suggestion[]=BOOT-5122||-|-|
suggestion[]=LYNIS|This release is old|-|-|
hardening_index=67
report_datetime_end=2026-11-01 18:00:00
`

func TestParseLynis(t *testing.T) {
	r, err := parseLynis(strings.NewReader(lynisSample))
	if err != nil {
		t.Fatal(err)
	}
	if r.Index != 67 || r.Version != "3.1.4" || len(r.Warnings) != 2 || len(r.Suggestions) != 3 || r.Suggestions[2].Test != "LYNIS" {
		t.Fatalf("%+v", r)
	}
	if w := r.Warnings[0]; w.Test != "AUTH-9286" || w.Details != "" || w.Solution != "" {
		t.Fatalf("a dash is no value: %+v", w)
	}
	if w := r.Warnings[1]; w.Test != "" || w.Text != "<img src=x onerror=alert(1)>" || w.Details != "/etc/x" || w.Solution != "Fix it" {
		t.Fatalf("a bad id goes, the text stays for the page to escape: %+v", w)
	}
	if s := r.Suggestions[0]; s.Solution != "a|b" {
		t.Fatalf("a bar in the last field: %+v", s)
	}
	if s := r.Suggestions[1]; strings.ContainsAny(s.Text, "\x1b\x00") {
		t.Fatalf("control characters: %q", s.Text)
	}
	b, _ := json.Marshal(r)
	for _, leak := range []string{"secret-host", "mohammed"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%s left the broker: %s", leak, b)
		}
	}
}

// A report cut short must never read as a machine with nothing to fix.
func TestParseLynisEmptyIsNotClean(t *testing.T) {
	for _, s := range []string{
		"",
		"# Lynis Report\n",
		"hardening_index=67\n",
		"report_datetime_end=x\n",
		"hardening_index=101\nreport_datetime_end=x\n",
		"hardening_index=-1\nreport_datetime_end=x\n",
		"hardening_index=abc\nreport_datetime_end=x\n",
	} {
		if _, err := parseLynis(strings.NewReader(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestParseLynisIsBounded(t *testing.T) {
	var b strings.Builder
	for range maxLynisFindings + 10 {
		b.WriteString("warning[]=AUTH-1|" + strings.Repeat("x", 2000) + "|-|-|\n")
	}
	b.WriteString("hardening_index=1\nreport_datetime_end=x\n")
	r, err := parseLynis(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != maxLynisFindings || !r.Truncated || len([]rune(r.Warnings[0].Text)) != maxLynisText {
		t.Fatalf("%d %v %d", len(r.Warnings), r.Truncated, len(r.Warnings[0].Text))
	}
}

// lynisBus records systemd calls and finishes a started unit at once.
type lynisBus struct {
	calls []string
	state string
	fail  string
	// onStart sees the moment a unit starts.
	onStart func()
}

func (b *lynisBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, _ ...any) error {
	name := method[strings.LastIndexByte(method, '.')+1:]
	b.calls = append(b.calls, name)
	if name == b.fail {
		return errors.New("refused")
	}
	if name == "StartUnit" && b.onStart != nil {
		b.onStart()
	}
	if name == "LoadUnit" {
		*(out[0].(*dbus.ObjectPath)) = "/unit"
	}
	return nil
}

func (b *lynisBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return map[string]dbus.Variant{
		"ActiveState":          dbus.MakeVariant(b.state),
		"StateChangeTimestamp": dbus.MakeVariant(uint64(time.Now().Add(time.Hour).UnixMicro())),
	}, nil
}

func (b *lynisBus) Close() error { return nil }

func lynisEnv(t *testing.T) (LynisConfig, *lynisBus, map[string]Method) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lynis")
	if err := os.WriteFile(bin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bus := &lynisBus{state: "inactive"}
	c := LynisConfig{
		Bus:    func(context.Context) (SystemBus, error) { return bus, nil },
		Output: filepath.Join(dir, "report.dat"), Binary: bin, UnitDir: dir, Poll: time.Millisecond,
	}
	m := map[string]Method{}
	for _, x := range LynisMethods(c) {
		m[x.Name] = x
	}
	return c, bus, m
}

func TestLynisMethodsAreClosed(t *testing.T) {
	_, _, m := lynisEnv(t)
	if len(m) != 3 {
		t.Fatalf("%d methods", len(m))
	}
	for _, n := range []string{"lynis.run", "lynis.schedule"} {
		if !m[n].Privileged || m[n].Action != ActionLynis || m[n].Check == nil {
			t.Fatalf("%s must ask polkit, after its params: %+v", n, m[n])
		}
	}
	if m["lynis.report"].Privileged {
		t.Fatal("reading must raise no dialog")
	}
	for _, bad := range []string{`{"args":"--upload"}`, `{"profile":"/tmp/x"}`, `[]`} {
		if err := m["lynis.run"].Check(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("lynis.run took %s", bad)
		}
	}
	for _, bad := range []string{`{}`, `{"every":""}`, `{"every":"daily"}`, `{"every":"weekly\n[Service]"}`, `{"every":"*-*-* 00:00:00"}`, `{"every":"weekly","x":1}`} {
		if err := m["lynis.schedule"].Check(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("lynis.schedule took %s", bad)
		}
	}
}

func TestLynisRun(t *testing.T) {
	c, bus, m := lynisEnv(t)
	ctx := context.Background()
	if err := os.WriteFile(c.Output, []byte(lynisSample), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := m["lynis.run"].Handle(ctx, Caller{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := v.(LynisState); !s.Installed || s.Report == nil || s.Report.Index != 67 || s.Report.Time == 0 {
		t.Fatalf("%+v", s)
	}
	if !strings.Contains(strings.Join(bus.calls, " "), "StartUnit") {
		t.Fatalf("%v", bus.calls)
	}

	bus.state = "failed"
	if _, err := m["lynis.run"].Handle(ctx, Caller{}, nil); err == nil || !strings.Contains(err.Error(), LynisUnit) {
		t.Fatalf("a failed unit: %v", err)
	}
}

func TestLynisReportWithoutTool(t *testing.T) {
	c, bus, m := lynisEnv(t)
	ctx := context.Background()
	v, err := m["lynis.report"].Handle(ctx, Caller{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := v.(LynisState); !s.Installed || s.Report != nil || s.Schedule != "off" {
		t.Fatalf("no report yet: %+v", s)
	}
	if err := os.Remove(c.Binary); err != nil {
		t.Fatal(err)
	}
	v, err = m["lynis.report"].Handle(ctx, Caller{}, nil)
	if err != nil || v.(LynisState).Installed {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := m["lynis.run"].Handle(ctx, Caller{}, nil); err == nil {
		t.Fatal("ran without the tool")
	}
	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"weekly"}`)); err == nil {
		t.Fatal("scheduled without the tool")
	}
	if len(bus.calls) != 0 {
		t.Fatalf("%v", bus.calls)
	}
}

func TestLynisSchedule(t *testing.T) {
	c, bus, m := lynisEnv(t)
	ctx := context.Background()
	path := filepath.Join(c.UnitDir, LynisTimer)

	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"weekly"}`)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\nOnCalendar=weekly\n") || c.schedule() != "weekly" {
		t.Fatalf("%s", b)
	}
	if got := strings.Join(bus.calls, " "); got != "Reload EnableUnitFiles RestartUnit" {
		t.Fatal(got)
	}
	bus.calls = nil
	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"monthly"}`)); err != nil {
		t.Fatal(err)
	}
	if c.schedule() != "monthly" {
		t.Fatal(c.schedule())
	}

	bus.calls = nil
	bus.fail = "DisableUnitFiles"
	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"off"}`)); err == nil {
		t.Fatal("a failure was hidden")
	}
	if c.schedule() != "monthly" {
		t.Fatal("the page must still show what will run")
	}
	bus.calls, bus.fail = nil, ""
	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"off"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || c.schedule() != "off" {
		t.Fatalf("%v %s", err, c.schedule())
	}
	bus.calls = nil
	if _, err := m["lynis.schedule"].Handle(ctx, Caller{}, json.RawMessage(`{"every":"off"}`)); err != nil || len(bus.calls) != 0 {
		t.Fatalf("off twice: %v %v", err, bus.calls)
	}
}

// A timer someone else wrote is not one of the table's rows.
func TestLynisScheduleReadsOnlyTheTable(t *testing.T) {
	c, _, _ := lynisEnv(t)
	if err := os.WriteFile(filepath.Join(c.UnitDir, LynisTimer), []byte("[Timer]\nOnCalendar=hourly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.schedule() != "off" {
		t.Fatal(c.schedule())
	}
}

func FuzzParseLynis(f *testing.F) {
	f.Add(lynisSample)
	f.Add("warning[]=|||||\nhardening_index=0\nreport_datetime_end=\n")
	f.Fuzz(func(t *testing.T, s string) {
		r, err := parseLynis(strings.NewReader(s))
		if err != nil {
			return
		}
		if r.Index < 0 || r.Index > 100 || len(r.Warnings) > maxLynisFindings || len(r.Suggestions) > maxLynisFindings {
			t.Fatalf("%+v", r)
		}
		for _, x := range append(r.Warnings, r.Suggestions...) {
			if x.Text == "" || (x.Test != "" && !lynisTestRe.MatchString(x.Test)) || strings.ContainsAny(x.Text+x.Details+x.Solution, "\x00\n\x1b") {
				t.Fatalf("%+v", x)
			}
		}
	})
}

func FuzzLynisSchedule(f *testing.F) {
	m := map[string]Method{}
	for _, x := range LynisMethods(LynisConfig{}) {
		m[x.Name] = x
	}
	f.Add(`{"every":"weekly"}`)
	f.Add(`{"every":"off","x":1}`)
	f.Fuzz(func(t *testing.T, s string) {
		if err := m["lynis.schedule"].Check(context.Background(), json.RawMessage(s)); err != nil {
			return
		}
		var v LynisScheduleParams
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		if v.Every != "off" && lynisCalendars[v.Every] == "" {
			t.Fatalf("took %q", v.Every)
		}
	})
}
