package posture

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/netinfo"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConfig(t *testing.T) {
	root := t.TempDir()
	write(t, root, "usr/sbin/sshd", "", 0o700)
	// Debian's layout: the drop-ins come first, so they win.
	write(t, root, "etc/ssh/sshd_config", `# comment
Include /etc/ssh/sshd_config.d/*.conf
PasswordAuthentication yes
PermitRootLogin yes
Port 22
Port 2222
ChallengeResponseAuthentication no
Match User backup
	PermitRootLogin yes
	PermitEmptyPasswords yes
`, 0o600)
	write(t, root, "etc/ssh/sshd_config.d/10-hardening.conf", "PasswordAuthentication no\nPermitRootLogin=\"prohibit-password\"\n", 0o600)
	write(t, root, "etc/ssh/sshd_config.d/20-secret.conf", "x", 0o000)
	st := readSSH(root)
	if !st.Installed || st.PasswordAuth != "no" || st.PermitRootLogin != "prohibit-password" || st.KbdInteractive != "no" || st.PermitEmptyPasswd != "no" {
		t.Errorf("values %+v", st)
	}
	if strings.Join(st.Ports, ",") != "22,2222" || !st.Match {
		t.Errorf("ports/match %+v", st)
	}
	if os.Geteuid() != 0 && (len(st.Unreadable) != 1 || st.Unreadable[0] != "/etc/ssh/sshd_config.d/20-secret.conf") {
		t.Errorf("unreadable %v", st.Unreadable)
	}
	// Nothing set: sshd's defaults.
	empty := t.TempDir()
	write(t, empty, "etc/ssh/sshd_config", "", 0o600)
	if d := readSSH(empty); d.Installed || d.PasswordAuth != "yes" || d.PermitRootLogin != "prohibit-password" || d.Ports[0] != "22" {
		t.Errorf("defaults %+v", d)
	}
	// An Include loop stops.
	loop := t.TempDir()
	write(t, loop, "etc/ssh/sshd_config", "Include sshd_config\nPermitRootLogin no\n", 0o600)
	if d := readSSH(loop); d.PermitRootLogin != "no" {
		t.Errorf("loop %+v", d)
	}
}

func TestScanPath(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	open := filepath.Join(dir, "open")
	for _, d := range []string{good, open} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(open, 0o777); err != nil { // #nosec G302 -- the finding under test
		t.Fatal(err)
	}
	write(t, good, "ok", "", 0o700)
	write(t, good, "data.txt", "", 0o600) // not a program
	write(t, good, "swap-me", "", 0o700)
	if err := os.Chmod(filepath.Join(good, "swap-me"), 0o777); err != nil { // #nosec G302 -- the finding under test
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(good, "swap-me"), filepath.Join(good, "alias")); err != nil {
		t.Fatal(err)
	}
	found, truncated := scanPath([]string{good, open, good + "/", "", "bin", "/nonexistent"}, uint32(os.Getuid())) // #nosec G115 -- a uid
	got := map[string]Writable{}
	for _, w := range found {
		got[w.Path] = w
	}
	if w := got[open]; !w.Dir || w.Why != "world" {
		t.Errorf("open dir %+v", w)
	}
	if w := got[filepath.Join(good, "swap-me")]; w.Dir || w.Why != "world" {
		t.Errorf("file %+v", w)
	}
	if w := got[filepath.Join(good, "alias")]; w.Link != filepath.Join(good, "swap-me") {
		t.Errorf("link %+v", w)
	}
	if got["."].Why != "relative" || got["bin"].Why != "relative" {
		t.Errorf("relative %+v", found)
	}
	if len(found) != 5 || truncated {
		t.Errorf("found %+v", found)
	}
}

func TestDebsecan(t *testing.T) {
	out := `CVE-2024-0001 libfoo (fixed, low urgency)
CVE-2024-0002 openssl (fixed, remotely exploitable, high urgency)
CVE-2024-0003 bash (remotely exploitable, medium urgency)
TEMP-0000000-ABCDEF zlib1g
garbage line here
CVE-2024-0004 <img> (fixed)
`
	list, trunc := parseSummary([]byte(out))
	if trunc || len(list) != 4 {
		t.Fatalf("%v %+v", trunc, list)
	}
	if list[0].ID != "CVE-2024-0002" || !list[0].Remote || !list[0].Fixed || list[0].Urgency != "high" {
		t.Errorf("order %+v", list)
	}

	root := t.TempDir()
	ctx := context.Background()
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		gotArgs = append([]string{name}, args...)
		return safeexec.Result{Stdout: []byte(out)}, nil
	}
	if v := runDebsecan(ctx, run, root); v.Installed || gotArgs != nil {
		t.Errorf("not installed: %+v %v", v, gotArgs)
	}
	write(t, root, "usr/bin/debsecan", "", 0o700)
	if v := runDebsecan(ctx, run, root); v.Error == "" {
		t.Errorf("no codename: %+v", v)
	}
	write(t, root, "etc/os-release", "NAME=\"Debian\"\nVERSION_CODENAME=trixie\n", 0o600)
	v := runDebsecan(ctx, run, root)
	if strings.Join(gotArgs, " ") != "debsecan --suite trixie --format summary" || v.Fixed != 2 || v.Remote != 2 || len(v.List) != 4 {
		t.Errorf("run %v %+v", gotArgs, v)
	}
	// The command's error is the last line of its stderr.
	fail := func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{Stderr: []byte("Traceback...\n  File x\nerror: cannot download https://security-tracker.debian.org/...")}, errors.New("exit status 1")
	}
	if v := runDebsecan(ctx, fail, root); !strings.HasPrefix(v.Error, "error: cannot download") {
		t.Errorf("error %+v", v)
	}
	if err := safeexecShape(); err != nil {
		t.Error(err)
	}
}

// safeexecShape checks that the arguments runDebsecan builds are the ones
// the allowlist accepts.
func safeexecShape() error {
	_, err := safeexec.Run(context.Background(), "debsecan", "--suite", "trixie", "--format", "summary")
	if err != nil && strings.Contains(err.Error(), "invalid") {
		return err
	}
	return nil
}

type memSettings struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *memSettings) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memSettings) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

func scanner(t *testing.T, root string, settings SettingsStore) *Scanner {
	t.Helper()
	write(t, root, "usr/sbin/sshd", "", 0o700)
	write(t, root, "usr/bin/debsecan", "", 0o700)
	write(t, root, "etc/os-release", "VERSION_CODENAME=trixie\n", 0o600)
	write(t, root, "etc/ssh/sshd_config", "PermitRootLogin yes\n", 0o600)
	return New(Config{
		Root: root, UID: os.Getuid(), Path: []string{""},
		Sockets: func() (netinfo.Sockets, error) {
			return netinfo.Sockets{Listeners: []netinfo.Endpoint{
				{Proto: "tcp", Port: 8080, Scope: netinfo.ScopeAll, User: "web"},
				{Proto: "tcp", Port: 631, Scope: "loopback", User: "root"},
			}}, nil
		},
		Firewalls:  func(context.Context) []netinfo.Firewall { return []netinfo.Firewall{{Name: "ufw", Installed: true}} },
		UnitActive: func(_ context.Context, unit string) bool { return unit == "ssh.service" },
		Updates: func(context.Context) ([]string, int, error) {
			return []string{"openssl", "libc6"}, 5, nil
		},
		Containers: func(context.Context) ([]Risky, error) {
			return []Risky{{Name: "plain", Running: true, Risks: []string{}}, {Name: "agent", Running: true, Risks: []string{"host-mount:/var/run/docker.sock"}},
				{Name: "net", Risks: []string{"host-network"}}, {Name: "clock", Running: true, Risks: []string{"host-mount:/etc/localtime"}}}, nil
		},
		Run: func(context.Context, string, ...string) (safeexec.Result, error) {
			return safeexec.Result{Stdout: []byte("CVE-2024-0002 openssl (fixed, remotely exploitable, high urgency)\nCVE-2024-0003 bash (low urgency)\n")}, nil
		},
		Settings: settings,
		Clock:    func() time.Time { return time.UnixMilli(1000) },
	})
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	s := scanner(t, root, &memSettings{m: map[string]json.RawMessage{}})
	r := s.Scan(context.Background())
	checks := map[string]Check{}
	var order []string
	for _, c := range r.Checks {
		checks[c.ID] = c
		order = append(order, c.ID)
	}
	if strings.Join(order, ",") != "listeners,firewall,ssh,updates,restart,cves,containers,path" {
		t.Fatalf("order %v", order)
	}
	want := map[string]string{
		CheckListeners: SevWarn, CheckFirewall: SevWarn, CheckSSH: SevCrit, CheckUpdates: SevCrit,
		CheckCVEs: StatusUnknown, CheckContainers: SevCrit, CheckPath: SevCrit,
	}
	for id, st := range want {
		if checks[id].Status != st {
			t.Errorf("%s: %s, want %s: %+v", id, checks[id].Status, st, checks[id])
		}
	}
	if f := checks[CheckListeners].Findings; len(f) != 1 || f[0].Subject != "TCP 8080 (web)" {
		t.Errorf("listeners %+v", f)
	}
	if f := checks[CheckSSH].Findings; f[0].Code != "ssh.root_login" || f[0].Severity != SevCrit {
		t.Errorf("ssh sorted worst first %+v", f)
	}
	if f := checks[CheckContainers].Findings; len(f) != 3 || f[0].Subject != "agent" || f[1].Code != "container.risky_stopped" || f[1].Severity != SevWarn || f[2].Subject != "clock" || f[2].Severity != SevInfo {
		t.Errorf("containers %+v", f)
	}
	if f := checks[CheckUpdates].Findings; f[0].Subject != "2" || strings.Join(f[0].Details, ",") != "libc6,openssl" || f[1].Subject != "3" {
		t.Errorf("updates %+v", f)
	}
	if r.SSH == nil || !r.SSH.Active || r.Counts[SevCrit] != 4 {
		t.Errorf("ssh %+v counts %v", r.SSH, r.Counts)
	}
}

func TestScanStates(t *testing.T) {
	// Nothing to check, or nothing answers.
	root := t.TempDir()
	s := New(Config{Root: root,
		Sockets: func() (netinfo.Sockets, error) { return netinfo.Sockets{}, errors.New("netlink: permission denied") },
		Firewalls: func(context.Context) []netinfo.Firewall {
			return []netinfo.Firewall{{Name: "nftables", Installed: true, Active: true}}
		},
		Updates:    func(context.Context) ([]string, int, error) { return nil, 0, nil },
		Containers: func(context.Context) ([]Risky, error) { return nil, ErrNoDocker },
	})
	r := s.Scan(context.Background())
	got := map[string]string{}
	for _, c := range r.Checks {
		got[c.ID] = c.Status
	}
	want := map[string]string{CheckListeners: StatusUnknown, CheckFirewall: StatusOK, CheckSSH: StatusOff, CheckUpdates: StatusOK,
		CheckCVEs: StatusOff, CheckContainers: StatusOff, CheckPath: StatusOK}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("%s: %s, want %s", id, got[id], st)
		}
	}
	if r.Checks[0].Error != "netlink: permission denied" {
		t.Errorf("error %+v", r.Checks[0])
	}
}

type audit struct{ e []store.AuditEntry }

func (a *audit) Audit(_ context.Context, e store.AuditEntry) error {
	a.e = append(a.e, e)
	return nil
}

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func TestCVEsHandlerAndCache(t *testing.T) {
	root := t.TempDir()
	settings := &memSettings{m: map[string]json.RawMessage{}}
	s := scanner(t, root, settings)
	au := &audit{}
	m := mux{http.NewServeMux()}
	s.Register(m, au)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/security/cves", nil))
	var v CVEs
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || len(v.List) != 2 || v.Time != 1000 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(au.e) != 1 || au.e[0].Action != "security.cves" || au.e[0].Result != "ok: 2 CVEs" {
		t.Errorf("audit %+v", au.e)
	}
	// The next scan shows the run: a remotely exploitable CVE with a fix.
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/security", nil))
	var r Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if c := r.Checks[5]; c.ID != CheckCVEs || c.Status != SevCrit || c.Findings[0].Details[0] != "CVE-2024-0002 (openssl)" {
		t.Errorf("cves %+v", c)
	}
	// A restart reads the kept run.
	again := scanner(t, root, settings)
	if v := again.CVEs(context.Background()); len(v.List) != 2 {
		t.Errorf("kept %+v", v)
	}

	// One run at a time.
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/security/cves", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("busy: %d", rec.Code)
	}
}

var regexpCode = regexp.MustCompile(`Code: "([a-z_]+\.[a-z_]+)"|code := "([a-z_]+\.[a-z_]+)"`)

func TestTexts(t *testing.T) {
	// Every code the checks use has a text.
	src, err := os.ReadFile("posture.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexpCode.FindAllStringSubmatch(string(src), -1) {
		code := m[1] + m[2]
		if _, ok := texts[code]; !ok {
			t.Errorf("no text for %q", code)
		}
	}
	for _, code := range []string{"path.world", "path.world_file", "path.owner", "path.owner_file", "path.relative", "path.truncated"} {
		if _, ok := texts[code]; !ok {
			t.Errorf("no text for %q", code)
		}
	}
	if got := Text(Finding{Code: "container.risky", Subject: "db", Details: []string{"privileged"}}); got != "db could take over the host: privileged" {
		t.Errorf("%q", got)
	}
	if CheckName(CheckCVEs) == CheckCVEs {
		t.Error("no name")
	}
}

func restartRoot(t *testing.T, kernels ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, k := range kernels {
		if err := os.WriteFile(filepath.Join(root, "boot", "vmlinuz-"+k), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestKernelFlavor(t *testing.T) {
	for in, want := range map[string]string{"6.12.48+deb13-amd64": "amd64", "6.12.48+deb13-rt-amd64": "rt-amd64", "6.1.0-18-amd64": "amd64", "6.1.0-18-cloud-amd64": "cloud-amd64"} {
		if got := kernelFlavor(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestNewestKernel(t *testing.T) {
	root := restartRoot(t, "6.12.9+deb13-amd64", "6.12.10+deb13-amd64", "6.12.99+deb13-rt-amd64", "6.12.8+deb13-amd64")
	if got := newestKernel(root, "6.12.9+deb13-amd64"); got != "6.12.10+deb13-amd64" {
		t.Fatal(got)
	}
	if got := newestKernel(root, "6.12.10+deb13-amd64"); got != "" {
		t.Fatal(got)
	}
	if got := newestKernel(root, "6.12.99+deb13-rt-amd64"); got != "" {
		t.Fatal(got)
	}
	if got := newestKernel(root, ""); got != "" {
		t.Fatal(got)
	}
}

func restartScanner(root string, scan func(context.Context) (RestartScan, error)) *Scanner {
	return New(Config{Root: root, Release: func() string { return "6.12.9+deb13-amd64" }, Needrestart: scan})
}

func TestRestartClean(t *testing.T) {
	s := restartScanner(restartRoot(t, "6.12.9+deb13-amd64"), func(context.Context) (RestartScan, error) { return RestartScan{Installed: true}, nil })
	r := s.Restart(context.Background(), true)
	if r.Reboot || !r.Scanned || len(r.Services) != 0 {
		t.Fatalf("%+v", r)
	}
	if c := restartCheck(r); c.Status != StatusOK || len(c.Findings) != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestRestartNeeded(t *testing.T) {
	root := restartRoot(t, "6.12.9+deb13-amd64", "6.12.10+deb13-amd64")
	if err := os.WriteFile(filepath.Join(root, "run", "reboot-required"), []byte("*** System restart required ***\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run", "reboot-required.pkgs"), []byte("libc6\nlinux-image-amd64\nlibc6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := restartScanner(root, func(context.Context) (RestartScan, error) {
		return RestartScan{Installed: true, Services: []string{"nginx.service"}, Sessions: []string{"me @ user manager service"}}, nil
	})
	r := s.Restart(context.Background(), true)
	if !r.Reboot || len(r.Packages) != 2 || r.Newest != "6.12.10+deb13-amd64" || len(r.Services) != 1 {
		t.Fatalf("%+v", r)
	}
	c := restartCheck(r)
	if c.Status != SevWarn || len(c.Findings) != 4 {
		t.Fatalf("%+v", c)
	}
	for _, f := range c.Findings {
		if Text(f) == "" || strings.HasPrefix(Text(f), f.Code) {
			t.Errorf("no text for %s", f.Code)
		}
	}
}

// A scan that failed or has not ended must not read as all clear.
func TestRestartUnknownIsSaid(t *testing.T) {
	root := restartRoot(t, "6.12.9+deb13-amd64")
	s := restartScanner(root, func(context.Context) (RestartScan, error) { return RestartScan{}, errors.New("no") })
	r := s.Restart(context.Background(), true)
	if r.Error == "" || restartCheck(r).Findings[0].Code != "restart.scan_failed" {
		t.Fatalf("%+v", r)
	}
	hold := make(chan struct{})
	s = restartScanner(root, func(context.Context) (RestartScan, error) { <-hold; return RestartScan{Installed: true}, nil })
	r = s.Restart(context.Background(), false)
	if r.Scanned || restartCheck(r).Findings[0].Code != "restart.scanning" {
		t.Fatalf("%+v", r)
	}
	close(hold)
	if r = s.Restart(context.Background(), true); !r.Scanned {
		t.Fatalf("%+v", r)
	}
	s = restartScanner(root, func(context.Context) (RestartScan, error) { return RestartScan{}, nil })
	if c := restartCheck(s.Restart(context.Background(), true)); c.Findings[0].Code != "restart.no_tool" {
		t.Fatalf("%+v", c)
	}
}

func TestRestartScanIsPaced(t *testing.T) {
	n := 0
	s := restartScanner(restartRoot(t), func(context.Context) (RestartScan, error) { n++; return RestartScan{Installed: true}, nil })
	for range 3 {
		s.Restart(context.Background(), true)
	}
	if n != 1 {
		t.Fatal(n)
	}
}

// Root is "" in production, and Join then drops it: without the leading
// slash these would be relative to the working directory, and never found.
func TestRestartPathsAreAbsoluteInProduction(t *testing.T) {
	root := restartRoot(t, "0.0.2-amd64")
	if err := os.WriteFile(filepath.Join(root, "run", "reboot-required.pkgs"), []byte("relative-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if got := newestKernel("", "0.0.1-amd64"); got == "0.0.2-amd64" {
		t.Error("read boot/ relative to the working directory")
	}
	if slices.Contains(rebootPackages(""), "relative-sentinel") {
		t.Error("read run/ relative to the working directory")
	}
}
