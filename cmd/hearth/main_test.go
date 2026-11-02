package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testToken = "hearth_ro_test" // #nosec G101 -- not a credential

// hearthd stands in for the daemon: it answers only with the right token.
func hearthd(t *testing.T, answers map[string]string) func(args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("the CLI sent %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "hearth")
	if err := os.MkdirAll(filepath.Join(dir, "tokens"), 0o700); err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	if err := os.WriteFile(filepath.Join(dir, "config.json"), fmt.Appendf(nil, `{"port":%d}`, port), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokens", "cli.token"), []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return cfg
		}
		return ""
	}
	return func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), args, env, &out, &errOut)
		return code, out.String(), errOut.String()
	}
}

func TestCommands(t *testing.T) {
	cli := hearthd(t, map[string]string{
		"/api/v1/alerts/events": `{"events":[{"rule_name":"Disk","instance":"/","severity":"warning","started_at":1700000000000},
			{"rule_name":"Old","severity":"info","started_at":1700000000000,"ended_at":1700000100000}]}`,
		"/api/v1/net/sockets": `{"listeners":[{"proto":"tcp","address":"127.0.0.1","port":631,"scope":"loopback","user":"root","owner":{"group_name":"cups"}}]}`,
		"/api/v1/services":    `{"units":[{"name":"ssh.service","scope":"system","active":"active","sub":"running"}]}`,
		"/api/v1/containers":  `{"state":"ok","containers":[{"name":"web","image":"nginx","state":"running","status":"Up 2 hours"}]}`,
		"/api/v1/metrics/live": `{"t":[1,2],"series":{"cpu.usage":[10,12.5],"load1":[0.5,null],"mem.used":[null,2147483648],
			"swap.used":[0,0],"disk.used:/home":[1024,2048]}}`,
	})
	for _, tc := range []struct {
		cmd  string
		want []string
	}{
		{"alerts", []string{"SEVERITY", "Disk", "open", "ended "}},
		{"ports", []string{"631", "cups", "loopback"}},
		{"units", []string{"ssh.service", "running"}},
		{"containers", []string{"web", "Up 2 hours"}},
		{"status", []string{"12.5%", "0.50", "2.0 GiB", "Used on /home", "2.0 KiB", "Open alerts    1"}},
	} {
		code, out, stderr := cli(tc.cmd)
		if code != 0 {
			t.Errorf("%s: exit %d: %s", tc.cmd, code, stderr)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: no %q in:\n%s", tc.cmd, w, out)
			}
		}
	}
	// --json passes the answer on, before or after the command.
	for _, args := range [][]string{{"--json", "units"}, {"units", "--json"}} {
		if _, out, _ := cli(args...); !strings.HasPrefix(out, `{"units":[`) {
			t.Errorf("%v: %s", args, out)
		}
	}
	if _, out, _ := cli("status", "--json"); !strings.HasPrefix(out, `{"metrics":{`) || !strings.Contains(out, `"alerts":{`) {
		t.Errorf("status --json: %s", out)
	}
}

func TestEscapeSequencesAreNotPrinted(t *testing.T) {
	cli := hearthd(t, map[string]string{
		"/api/v1/containers": `{"state":"ok","containers":[{"name":"a\u001b]0;pwned\u0007b\u009b31m","image":"x\ty\nz","state":"\u202eevil","status":""}]}`,
	})
	_, out, _ := cli("containers")
	if strings.ContainsAny(out, "\x1b\x07\u009b\u202e") {
		t.Errorf("control characters reached the terminal: %q", out)
	}
	if strings.Count(out, "\n") != 2 {
		t.Errorf("a name broke the table: %q", out)
	}
}

func TestErrorsSayWhatToDo(t *testing.T) {
	cli := hearthd(t, map[string]string{})
	if code, _, stderr := cli("units"); code != 1 || !strings.Contains(stderr, "read-only token") {
		t.Errorf("refused route: %d %q", code, stderr)
	}
	if code, _, stderr := cli("reboot"); code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Errorf("unknown command: %d %q", code, stderr)
	}
	if code, _, _ := cli(); code != 2 {
		t.Errorf("no command: %d", code)
	}
	if code, out, _ := cli("--help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("help: %d %q", code, out)
	}
	other := filepath.Join(t.TempDir(), "other.token")
	if err := os.WriteFile(other, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := cli("--token-file", other, "units"); code != 1 || !strings.Contains(stderr, "refused") {
		t.Errorf("wrong token: %d %q", code, stderr)
	}
	if err := os.Chmod(other, 0o644); err != nil { // #nosec G302 -- the case under test
		t.Fatal(err)
	}
	if code, _, stderr := cli("--token-file", other, "units"); code != 1 || !strings.Contains(stderr, "chmod 600") {
		t.Errorf("readable token: %d %q", code, stderr)
	}
	if code, _, stderr := cli("--token-file", other+"-none", "units"); code != 1 || !strings.Contains(stderr, "Tokens page") {
		t.Errorf("no token: %d %q", code, stderr)
	}
}

func TestBadPortInConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"port":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPort(path); err == nil {
		t.Error("a port under 1024 was accepted")
	}
}

func FuzzClean(f *testing.F) {
	for _, s := range []string{"web", "\x1b[31m", "a\nb", "\u202e", "\xff", "مرحبا"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, r := range clean(s) {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				t.Fatalf("%q survived in %q", r, clean(s))
			}
		}
	})
}
