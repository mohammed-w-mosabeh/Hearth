package dropin

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

func TestValidate(t *testing.T) {
	good := Directives{"MemoryMax": "512M", "CPUQuota": "150%", "Restart": "on-failure", "RestartSec": "5s",
		"Nice": "-5", "IOWeight": "100", "TimeoutStopSec": "90"}
	if err := Validate(good); err != nil {
		t.Fatal(err)
	}
	for _, d := range []Directives{
		{"ExecStart": "/bin/sh"}, {"ExecStartPre": "/bin/true"}, {"ExecStop": "/bin/true"},
		{"Environment": "A=1"}, {"EnvironmentFile": "/etc/x"}, {"User": "root"}, {"Group": "root"},
		{"WorkingDirectory": "/"}, {"RootDirectory": "/"}, {"BindPaths": "/:/x"}, {"ReadWritePaths": "/"},
		{"memorymax": "1G"}, {"MemoryMax ": "1G"}, {"": ""},
		{"MemoryMax": "512M\nExecStart=/bin/sh"}, {"MemoryMax": "512M\n"}, {"MemoryMax": "0"}, {"MemoryMax": "-1"},
		{"MemoryMax": "101%"}, {"MemoryMax": ""}, {"CPUQuota": "0%"}, {"CPUQuota": "50"},
		{"Restart": "sometimes"}, {"RestartSec": "infinity"}, {"RestartSec": "1 s"},
		{"Nice": "20"}, {"Nice": "-21"}, {"Nice": "+1"}, {"Nice": "01"},
		{"IOWeight": "0"}, {"IOWeight": "10001"}, {"TimeoutStopSec": "1d"},
	} {
		if Validate(d) == nil {
			t.Errorf("accepted %q", d)
		}
	}
}

func TestValidUnit(t *testing.T) {
	for _, u := range []string{"cups.service", "getty@tty1.service"} {
		if err := ValidUnit(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"", "cups", "cups.socket", "cups.timer", "getty@.service", "../x.service", "a/b.service",
		"-x.service", "..service", "hearth-broker.service", "hearthd.service", "hearth-job-1.service", "a\nb.service"} {
		if ValidUnit(u) == nil {
			t.Errorf("accepted %q", u)
		}
	}
}

func TestWriteReadRemove(t *testing.T) {
	dir := t.TempDir()
	want := Directives{"MemoryMax": "1G", "Restart": "always"}
	if err := Write(dir, "a.service", want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.service.d", FileName)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("%v %v", fi, err)
	}
	if got, err := Read(dir, "a.service"); err != nil || !maps.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- a test's own temp file
	if !strings.Contains(string(data), "[Service]\nMemoryMax=1G\nRestart=always\n") {
		t.Fatalf("%s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	if err := Write(dir, "a.service", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory stayed: %v", err)
	}
	if got, err := Read(dir, "a.service"); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestRemoveKeepsOtherDropIns(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "a.service", Directives{"Nice": "1"}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "a.service.d", "local.conf")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "a.service", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
}

func TestLinksRefused(t *testing.T) {
	dir, victim := t.TempDir(), t.TempDir()
	if err := os.Symlink(victim, filepath.Join(dir, "a.service.d")); err != nil {
		t.Fatal(err)
	}
	if Write(dir, "a.service", Directives{"Nice": "1"}) == nil {
		t.Error("followed a linked directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "b.service.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(victim, "x"), filepath.Join(dir, "b.service.d", FileName)); err != nil {
		t.Fatal(err)
	}
	if Write(dir, "b.service", Directives{"Nice": "1"}) == nil {
		t.Error("replaced a linked file")
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Errorf("wrote through a link: %v", entries)
	}
}

func TestParseKeepsOnlyTheAllowlist(t *testing.T) {
	got := Parse([]byte("[Service]\nExecStart=/bin/sh\nMemoryMax=1G\nNice=99\nUser=root\nRestart=<img>\n"))
	if !maps.Equal(got, Directives{"MemoryMax": "1G"}) {
		t.Fatalf("%v", got)
	}
}

type fakeReload struct{ calls int }

func (f *fakeReload) Call(_ context.Context, _ string, _ dbus.ObjectPath, m string, _ []any, _ ...any) error {
	if strings.HasSuffix(m, ".Reload") {
		f.calls++
	}
	return nil
}
func (f *fakeReload) Close() error { return nil }

func TestBrokerMethod(t *testing.T) {
	dir := t.TempDir()
	rel := &fakeReload{}
	m := BrokerMethods(dir, func(context.Context) (Reloader, error) { return rel, nil })[0]
	if m.Name != "unit.override" || !m.Privileged || m.Action != ActionOverride || m.Check == nil {
		t.Fatalf("%+v", m)
	}
	for _, p := range []string{
		`{"unit":"a.service","directives":{"ExecStart":"/bin/sh"}}`,
		`{"unit":"a.service","directives":{"Environment":"LD_PRELOAD=/tmp/x"}}`,
		`{"unit":"a.service","directives":{"User":"root"}}`,
		`{"unit":"a.service","directives":{"WorkingDirectory":"/root"}}`,
		`{"unit":"a.service","directives":{"MemoryMax":"1G\nExecStart=/bin/sh"}}`,
		`{"unit":"../../etc/cron.d/x.service","directives":{"Nice":"1"}}`,
		`{"unit":"a.socket","directives":{"Nice":"1"}}`,
		`{"unit":"hearth-broker.service","directives":{"Nice":"1"}}`,
		`{"unit":"a.service","directives":{},"path":"/etc/passwd"}`,
		`not json`,
	} {
		err := m.Check(context.Background(), json.RawMessage(p))
		var be *broker.Error
		if !errors.As(err, &be) || be.Code != broker.CodeBadParams {
			t.Errorf("%s: %v", p, err)
		}
		if _, err := m.Handle(context.Background(), broker.Caller{}, json.RawMessage(p)); err == nil {
			t.Errorf("handled %s", p)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 || rel.calls != 0 {
		t.Fatalf("refused calls changed something: %v, %d reloads", entries, rel.calls)
	}
	p := json.RawMessage(`{"unit":"a.service","directives":{"MemoryMax":"1G"}}`)
	if err := m.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Handle(context.Background(), broker.Caller{}, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(dir, "a.service"); got["MemoryMax"] != "1G" || rel.calls != 1 {
		t.Fatalf("%v, %d reloads", got, rel.calls)
	}
}

// Whatever passes renders to the allowlist and nothing else, one line each.
func FuzzValidate(f *testing.F) {
	f.Add("MemoryMax", "512M")
	f.Add("MemoryMax", "1G\nExecStart=/bin/sh")
	f.Add("ExecStart", "/bin/sh")
	f.Add("Nice", "-20")
	f.Add("Restart", "on-failure")
	f.Fuzz(func(t *testing.T, k, v string) {
		d := Directives{k: v}
		if Validate(d) != nil {
			return
		}
		if _, known := allowed[k]; !known || strings.ContainsAny(v, "\x00\n\r\t \\\"'$;=/") {
			t.Fatalf("accepted %q=%q", k, v)
		}
		if got := Parse(Render(d)); !maps.Equal(got, d) {
			t.Fatalf("%q=%q came back as %v", k, v, got)
		}
	})
}

func FuzzValidUnit(f *testing.F) {
	f.Add("cups.service")
	f.Add("../x.service")
	f.Add("getty@tty1.service")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidUnit(s) != nil {
			return
		}
		if filepath.Base(s) != s || !strings.HasSuffix(s, ".service") || strings.HasPrefix(s, "hearth") || strings.ContainsAny(s, "/\x00\n") {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("[Service]\nMemoryMax=1G\nExecStart=/bin/sh\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := Validate(Parse(data)); err != nil {
			t.Fatal(err)
		}
	})
}
