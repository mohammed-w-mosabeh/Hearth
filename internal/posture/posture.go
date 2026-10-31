// Package posture checks how exposed this machine is (SPEC 18, Security
// posture): listeners on every address, the firewall, the SSH server's
// configuration, pending security updates, known CVEs (debsecan),
// containers that could take over the host, and PATH entries others can
// write. It only reads; fixing is left to the user.
package posture

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/netinfo"
)

// Severities, from least to most worrying. A check's status is the worst
// severity of its findings, "ok" without any, "unknown" when it could not
// run and "off" when it does not apply (no SSH server, no Docker).
const (
	SevInfo = "info"
	SevWarn = "warning"
	SevCrit = "critical"

	StatusOK      = "ok"
	StatusUnknown = "unknown"
	StatusOff     = "off"
)

// Check IDs, in the order the page shows them.
const (
	CheckListeners  = "listeners"
	CheckFirewall   = "firewall"
	CheckSSH        = "ssh"
	CheckUpdates    = "updates"
	CheckRestart    = "restart"
	CheckCVEs       = "cves"
	CheckContainers = "containers"
	CheckPath       = "path"
)

// Finding is one thing a check found. Code names the message (the UI's
// "security.f.<code>", Text here); Subject and Details fill it in.
type Finding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Subject  string   `json:"subject,omitempty"`
	Details  []string `json:"details,omitempty"`
}

// Check is the result of one check.
type Check struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	Findings []Finding `json:"findings"`
	Error    string    `json:"error,omitempty"`
}

// Report is a full posture.
type Report struct {
	Time   int64   `json:"time"` // unix ms
	Checks []Check `json:"checks"`
	SSH    *SSH    `json:"ssh,omitempty"`
	CVEs   CVEs    `json:"cves"`
	// Counts of checks by status.
	Counts map[string]int `json:"counts"`
}

// Risky is a container with SECURITY S12 risks.
type Risky struct {
	Name    string   `json:"name"`
	Running bool     `json:"running"`
	Risks   []string `json:"risks"`
}

// ErrNoDocker is returned by Config.Containers when Docker is not there.
var ErrNoDocker = errors.New("posture: Docker is not available")

// SettingsStore keeps the last CVE run (store.DB in production).
type SettingsStore interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

// Config configures a Scanner. Nil functions skip their check (unknown).
type Config struct {
	Root string // prefix for files (tests); "" in production
	UID  int
	Path []string // directories searched for programs

	Sockets    func() (netinfo.Sockets, error)
	Firewalls  func(ctx context.Context) []netinfo.Firewall
	UnitActive func(ctx context.Context, unit string) bool
	// Updates returns the names of pending security updates and the
	// count of all pending updates.
	Updates    func(ctx context.Context) (security []string, total int, err error)
	Containers func(ctx context.Context) ([]Risky, error)
	// Release is the running kernel's; Needrestart is the optional scan
	// through the broker.
	Release     func() string
	Needrestart func(ctx context.Context) (RestartScan, error)
	Run         Runner
	Settings    SettingsStore
	Log         *slog.Logger
	Clock       func() time.Time
}

// Scanner runs the checks.
type Scanner struct {
	cfg     Config
	restart restartCache

	mu      sync.Mutex
	cves    CVEs
	loaded  bool
	running bool
}

// New returns a Scanner.
func New(cfg Config) *Scanner {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Scanner{cfg: cfg}
}

const cvesKey = "security.cves"

// checkTimeout bounds each check but debsecan.
const checkTimeout = 15 * time.Second

// Scan runs every check but debsecan, whose last result it includes.
func (s *Scanner) Scan(ctx context.Context) Report {
	r := Report{Time: s.cfg.Clock().UnixMilli(), Counts: map[string]int{}}

	// The firewall first: listeners are less worrying behind one.
	fwCheck, fwActive := s.firewall(ctx)

	var wg sync.WaitGroup
	results := map[string]Check{CheckFirewall: fwCheck}
	var mu sync.Mutex
	set := func(c Check) {
		mu.Lock()
		results[c.ID] = c
		mu.Unlock()
	}
	run := func(f func(context.Context) Check) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			set(f(cctx))
		}()
	}
	run(func(context.Context) Check { return s.listeners(fwActive) })
	run(func(ctx context.Context) Check {
		c, st := s.ssh(ctx)
		mu.Lock()
		r.SSH = st
		mu.Unlock()
		return c
	})
	run(s.updates)
	run(func(ctx context.Context) Check { return restartCheck(s.Restart(ctx, true)) })
	run(s.containers)
	run(func(context.Context) Check { return s.path() })
	wg.Wait()

	r.CVEs = s.CVEs(ctx)
	results[CheckCVEs] = cveCheck(r.CVEs)
	for _, id := range []string{CheckListeners, CheckFirewall, CheckSSH, CheckUpdates, CheckRestart, CheckCVEs, CheckContainers, CheckPath} {
		c := results[id]
		if c.Findings == nil {
			c.Findings = []Finding{}
		}
		sort.SliceStable(c.Findings, func(i, j int) bool { return sevRank(c.Findings[i].Severity) > sevRank(c.Findings[j].Severity) })
		r.Checks = append(r.Checks, c)
		r.Counts[c.Status]++
	}
	return r
}

func sevRank(s string) int {
	switch s {
	case SevCrit:
		return 3
	case SevWarn:
		return 2
	case SevInfo:
		return 1
	}
	return 0
}

// finish sets c's status from its findings.
func finish(c Check) Check {
	if c.Status != "" {
		return c
	}
	c.Status = StatusOK
	worst := 0
	for _, f := range c.Findings {
		if r := sevRank(f.Severity); r > worst && r >= sevRank(SevWarn) {
			worst, c.Status = r, f.Severity
		}
	}
	return c
}

func unknown(id string, err error) Check {
	c := Check{ID: id, Status: StatusUnknown, Findings: []Finding{}}
	if err != nil {
		c.Error = oneLine(err.Error(), 300)
	}
	return c
}

func (s *Scanner) firewall(ctx context.Context) (Check, bool) {
	if s.cfg.Firewalls == nil {
		return unknown(CheckFirewall, nil), false
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	c := Check{ID: CheckFirewall}
	var active, installed []string
	for _, f := range s.cfg.Firewalls(cctx) {
		if f.Active {
			active = append(active, f.Name)
		} else if f.Installed {
			installed = append(installed, f.Name)
		}
	}
	switch {
	case len(active) > 0:
		c.Findings = append(c.Findings, Finding{Code: "firewall.active", Severity: SevInfo, Subject: strings.Join(active, ", ")})
	case len(installed) > 0:
		c.Findings = append(c.Findings, Finding{Code: "firewall.inactive", Severity: SevWarn, Subject: strings.Join(installed, ", ")})
	default:
		c.Findings = append(c.Findings, Finding{Code: "firewall.none", Severity: SevWarn})
	}
	return finish(c), len(active) > 0
}

func (s *Scanner) listeners(firewall bool) Check {
	if s.cfg.Sockets == nil {
		return unknown(CheckListeners, nil)
	}
	socks, err := s.cfg.Sockets()
	if err != nil {
		return unknown(CheckListeners, err)
	}
	c := Check{ID: CheckListeners}
	sev := SevWarn
	if firewall {
		sev = SevInfo // reachable only as far as the firewall's rules allow
	}
	labels := make([]string, 0)
	for l := range netinfo.Exposed(socks) {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		c.Findings = append(c.Findings, Finding{Code: "listener.exposed", Severity: sev, Subject: l})
	}
	return finish(c)
}

func (s *Scanner) ssh(ctx context.Context) (Check, *SSH) {
	st := readSSH(s.cfg.Root)
	if !st.Installed {
		return Check{ID: CheckSSH, Status: StatusOff, Findings: []Finding{}}, nil
	}
	if s.cfg.UnitActive != nil {
		st.Active = s.cfg.UnitActive(ctx, "ssh.service")
	}
	c := Check{ID: CheckSSH}
	// A stopped server is a risk only once it starts.
	limit := func(sev string) string {
		if !st.Active && sev == SevCrit {
			return SevWarn
		}
		return sev
	}
	add := func(code, sev, subject string) {
		c.Findings = append(c.Findings, Finding{Code: code, Severity: limit(sev), Subject: subject})
	}
	if !st.Active {
		add("ssh.inactive", SevInfo, "")
	} else {
		add("ssh.active", SevInfo, strings.Join(st.Ports, ", "))
	}
	if st.PermitRootLogin == "yes" {
		add("ssh.root_login", SevCrit, st.PermitRootLogin)
	}
	if st.PermitEmptyPasswd == "yes" {
		add("ssh.empty_passwords", SevCrit, "")
	}
	if st.PasswordAuth == "yes" {
		add("ssh.password_auth", SevWarn, "")
	}
	if st.KbdInteractive == "yes" && st.PasswordAuth != "yes" {
		add("ssh.kbd_interactive", SevInfo, "")
	}
	if len(st.Unreadable) > 0 {
		c.Findings = append(c.Findings, Finding{Code: "ssh.unreadable", Severity: SevInfo, Details: st.Unreadable})
	}
	if st.Match {
		add("ssh.match", SevInfo, "")
	}
	return finish(c), &st
}

func (s *Scanner) updates(ctx context.Context) Check {
	if s.cfg.Updates == nil {
		return unknown(CheckUpdates, nil)
	}
	sec, total, err := s.cfg.Updates(ctx)
	if err != nil {
		return unknown(CheckUpdates, err)
	}
	c := Check{ID: CheckUpdates}
	if len(sec) > 0 {
		sort.Strings(sec)
		c.Findings = append(c.Findings, Finding{Code: "updates.security", Severity: SevCrit, Subject: itoa(len(sec)), Details: firstN(sec, 50)})
	}
	if other := total - len(sec); other > 0 {
		c.Findings = append(c.Findings, Finding{Code: "updates.other", Severity: SevInfo, Subject: itoa(other)})
	}
	return finish(c)
}

// Risks that hand the host to whoever controls the container.
var criticalRisks = map[string]bool{"privileged": true, "host-pid": true, "host-mount:/": true}

// timeMounts are the host's time zone files, which many images mount
// (read-only, as a rule) to show local time. SECURITY S12 still counts
// them, so the broker refuses a shell there; here they are only noted.
var timeMounts = map[string]bool{"host-mount:/etc/localtime": true, "host-mount:/etc/timezone": true}

func (s *Scanner) containers(ctx context.Context) Check {
	if s.cfg.Containers == nil {
		return unknown(CheckContainers, nil)
	}
	list, err := s.cfg.Containers(ctx)
	if errors.Is(err, ErrNoDocker) {
		return Check{ID: CheckContainers, Status: StatusOff, Findings: []Finding{}}
	}
	if err != nil {
		return unknown(CheckContainers, err)
	}
	c := Check{ID: CheckContainers}
	for _, k := range list {
		if len(k.Risks) == 0 {
			continue
		}
		sev := SevInfo
		for _, r := range k.Risks {
			switch {
			case criticalRisks[r] || strings.HasSuffix(r, "/docker.sock"):
				sev = SevCrit
			case !timeMounts[r] && sev != SevCrit:
				sev = SevWarn
			}
		}
		code := "container.risky"
		if !k.Running {
			code = "container.risky_stopped"
		}
		c.Findings = append(c.Findings, Finding{Code: code, Severity: sev, Subject: k.Name, Details: k.Risks})
	}
	return finish(c)
}

func (s *Scanner) path() Check {
	found, truncated := scanPath(s.cfg.Path, uint32(s.cfg.UID)) // #nosec G115 -- a uid
	c := Check{ID: CheckPath}
	for _, w := range found {
		sev := SevCrit
		if w.Why == "owner" {
			sev = SevWarn
		}
		code := "path." + w.Why
		if !w.Dir {
			code += "_file"
		}
		var details []string
		if w.Mode != "" {
			details = append(details, w.Mode)
		}
		if w.Link != "" {
			details = append(details, "→ "+w.Link)
		}
		c.Findings = append(c.Findings, Finding{Code: code, Severity: sev, Subject: w.Path, Details: details})
	}
	if truncated {
		c.Findings = append(c.Findings, Finding{Code: "path.truncated", Severity: SevInfo, Subject: itoa(maxPathFindings)})
	}
	return finish(c)
}

func cveCheck(v CVEs) Check {
	c := Check{ID: CheckCVEs}
	switch {
	case !v.Installed:
		return Check{ID: CheckCVEs, Status: StatusOff, Findings: []Finding{{Code: "cves.not_installed", Severity: SevInfo}}}
	case v.Error != "":
		return unknown(CheckCVEs, errors.New(v.Error))
	case v.Time == 0:
		return Check{ID: CheckCVEs, Status: StatusUnknown, Findings: []Finding{{Code: "cves.not_run", Severity: SevInfo}}}
	}
	if v.Fixed > 0 {
		sev := SevWarn
		var remote []string
		for _, x := range v.List {
			if x.Fixed && x.Remote {
				sev = SevCrit
				remote = append(remote, x.ID+" ("+x.Package+")")
			}
		}
		c.Findings = append(c.Findings, Finding{Code: "cves.fixed", Severity: sev, Subject: itoa(v.Fixed), Details: firstN(remote, 20)})
	}
	if open := len(v.List) - v.Fixed; open > 0 {
		c.Findings = append(c.Findings, Finding{Code: "cves.open", Severity: SevInfo, Subject: itoa(open)})
	}
	return finish(c)
}

// CVEs returns the last debsecan run (from the settings after a restart).
func (s *Scanner) CVEs(ctx context.Context) CVEs {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded && s.cfg.Settings != nil {
		s.loaded = true
		if raw, ok, err := s.cfg.Settings.Setting(ctx, cvesKey); err == nil && ok {
			_ = json.Unmarshal(raw, &s.cves)
		}
	}
	v := s.cves
	// Whether debsecan is installed is current, not as of the last run.
	v.Installed = exists(s.cfg.Root + "/usr/bin/debsecan")
	if v.List == nil {
		v.List = []CVE{}
	}
	return v
}

// ErrBusy is returned while debsecan is already running.
var ErrBusy = errors.New("posture: debsecan is already running")

// RunCVEs runs debsecan now and keeps the result.
func (s *Scanner) RunCVEs(ctx context.Context) (CVEs, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return CVEs{}, ErrBusy
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	if s.cfg.Run == nil {
		return CVEs{}, errors.New("posture: no runner")
	}
	v := runDebsecan(ctx, s.cfg.Run, s.cfg.Root)
	v.Time = s.cfg.Clock().UnixMilli()
	s.mu.Lock()
	s.cves, s.loaded = v, true
	s.mu.Unlock()
	if s.cfg.Settings != nil && v.Installed {
		if b, err := json.Marshal(v); err == nil {
			if err := s.cfg.Settings.SetSetting(ctx, cvesKey, b); err != nil {
				s.cfg.Log.Warn("posture: cannot keep the CVE list", "err", err)
			}
		}
	}
	return v, nil
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }
