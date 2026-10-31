package safeexec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// desktopEnv is what a browser needs from the user's session.
var desktopEnv = []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
	"XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "XDG_DATA_HOME", "XDG_DATA_DIRS", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS",
	"DESKTOP_SESSION", "BROWSER", "HOME"}

// allowlist is the complete set of programs Hearth may run. Adding an entry
// needs a validator that accepts only the argument shapes the caller uses.
var allowlist = map[string]Spec{
	// xdg-open and gio open <http(s) URL>: the launcher's fallbacks when no
	// desktop portal answers (package launcher). Both go through Start, so
	// the browser outlives hearthd open. Never from inside hearthd.service,
	// whose sandbox the browser would inherit.
	"xdg-open": {
		Paths:    []string{"/usr/bin/xdg-open"},
		Validate: exactly(HTTPURL),
		Env:      desktopEnv,
	},
	"gio-open": {
		Paths:    []string{"/usr/bin/gio"},
		Validate: exactly(Literal("open"), HTTPURL),
		Env:      desktopEnv,
	},
	// nvidia-smi with one fixed query (collect/gpu.go): NVIDIA exposes no
	// usable sysfs metrics, and NVML would need cgo.
	"nvidia-smi": {
		Paths: []string{"/usr/bin/nvidia-smi"},
		Validate: exactly(
			Literal("--query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw"),
			Literal("--format=csv,noheader,nounits"),
		),
		Timeout: 2 * time.Second,
	},
	// trivy scanning one image for known vulnerabilities (optional, if
	// installed; Docker images page). One fixed shape: vulnerabilities
	// only, quiet, TrivyTemplate output, the cache under Hearth's data
	// directory (hearthd's home is read-only), then the image.
	"trivy-image": {
		Paths: []string{"/usr/bin/trivy", "/usr/local/bin/trivy"},
		Validate: exactly(
			Literal("image"), Literal("--quiet"), Literal("--scanners=vuln"), Literal("--timeout=9m"),
			Literal("--format=template"), Literal("--template="+TrivyTemplate), TrivyCacheDir, ImageRef,
		),
		Env:     []string{"HOME", "XDG_RUNTIME_DIR"},
		Timeout: 10 * time.Minute,
	},
	// debsecan listing the CVEs that affect the installed packages
	// (optional, if installed; Security page, only when the user asks:
	// it fetches the Debian security tracker's data, SECURITY S21c). One
	// shape: this release's suite, the one-line-per-CVE summary.
	"debsecan": {
		Paths:    []string{"/usr/bin/debsecan"},
		Validate: exactly(Literal("--suite"), DebianSuite, Literal("--format"), Literal("summary")),
		Env:      []string{"HOME"},
		Timeout:  2 * time.Minute,
	},
	// apt-config reading the two automatic-update settings (Security page,
	// SECURITY S21e). `shell` returns only these keys; `dump` would also
	// hand over any proxy credentials.
	"apt-config-periodic": {
		Paths: []string{"/usr/bin/apt-config"},
		Validate: exactly(Literal("shell"), Literal("U"), Literal("APT::Periodic::Unattended-Upgrade"),
			Literal("L"), Literal("APT::Periodic::Update-Package-Lists")),
		Timeout: 5 * time.Second,
	},
	"trivy-version": {
		Paths:    []string{"/usr/bin/trivy", "/usr/local/bin/trivy"},
		Validate: exactly(Literal("--version")),
		Timeout:  5 * time.Second,
	},
	// apt-mark hold|unhold <package[:arch]>, run by hearth-broker as root
	// (broker pkg.hold, after polkit). PackageKit has no hold.
	"apt-mark-hold": {
		Paths:    []string{"/usr/bin/apt-mark"},
		Validate: exactly(OneOf("hold", "unhold"), DebPackageArch),
		Timeout:  30 * time.Second,
	},
	// smartctl reading one whole disk's report as JSON, or starting one of
	// its self-tests, run by hearth-broker as root (SECURITY S16f). -n
	// standby: a read never spins up a sleeping disk.
	"smartctl-read": {
		Paths:    []string{"/usr/sbin/smartctl"},
		Validate: exactly(Literal("-j"), Literal("-a"), Literal("-n"), Literal("standby"), WholeDisk),
		Timeout:  20 * time.Second,
	},
	"smartctl-test": {
		Paths:    []string{"/usr/sbin/smartctl"},
		Validate: exactly(Literal("-j"), Literal("-t"), OneOf("short", "long"), WholeDisk),
		Timeout:  20 * time.Second,
	},
	// journalctl --vacuum-size=<kept>, run by hearth-broker as root (broker
	// journal.vacuum, after polkit, SECURITY S16c).
	"journal-vacuum": {
		Paths:    []string{"/usr/bin/journalctl", "/bin/journalctl"},
		Validate: exactly(OneOf("--vacuum-size=100M", "--vacuum-size=200M", "--vacuum-size=500M", "--vacuum-size=1000M")),
		Timeout:  2 * time.Minute,
	},
	// apropos listing every man page of sections 1, 8 and 6 with its
	// one-line summary (Tools page descriptions; tools are never run).
	"apropos-all": {
		Paths:    []string{"/usr/bin/apropos"},
		Validate: exactly(Literal("-s"), Literal("1:8:6"), Literal("-l"), Literal(".")),
		Timeout:  15 * time.Second,
	},
	// man showing one listed tool's page as plain text (Tools page).
	"man-page": {
		Paths:    []string{"/usr/bin/man"},
		Validate: exactly(Literal("-P"), Literal("cat"), Literal("--"), ProgramName),
		Timeout:  10 * time.Second,
	},
	// systemd-analyze checking a schedule and listing its next runs
	// (Scheduler form). Pure computation, nothing is changed.
	"calendar-check": {
		Paths:    []string{"/usr/bin/systemd-analyze"},
		Validate: exactly(Literal("calendar"), Literal("--iterations=5"), Literal("--"), CalendarExpr),
		Timeout:  5 * time.Second,
	},
	"timespan-check": {
		Paths:    []string{"/usr/bin/systemd-analyze"},
		Validate: exactly(Literal("timespan"), Literal("--"), TimeSpan),
		Timeout:  5 * time.Second,
	},
	// journalctl reading a scheduled job's recent runs (Scheduler
	// history): fixed fields and line count, one hearth-job's matches.
	"journalctl-job": {
		Paths:    []string{"/usr/bin/journalctl", "/bin/journalctl"},
		Validate: jobJournalArgs,
		Timeout:  5 * time.Second,
	},
	// journalctl reading one unit's recent entries as JSON (services
	// journal viewer). Only the shape journalArgs accepts.
	"journalctl-unit": {
		Paths:    []string{"/usr/bin/journalctl", "/bin/journalctl"},
		Validate: journalArgs,
		Timeout:  5 * time.Second,
	},
	// journalctl reading the whole journal, newest first (Journal page).
	// Only the shape journalQueryArgs accepts.
	"journalctl-query": {
		Paths:    []string{"/usr/bin/journalctl", "/bin/journalctl"},
		Validate: journalQueryArgs,
		Timeout:  10 * time.Second,
	},
	// journalctl listing the boots it has logs of (Journal page).
	"journalctl-boots": {
		Paths:    []string{"/usr/bin/journalctl", "/bin/journalctl"},
		Validate: exactly(Literal("--list-boots"), Literal("--output=json"), Literal("--no-pager")),
		Timeout:  5 * time.Second,
	},
	// systemd-analyze on the last boot (Journal page): times, the units
	// by how long they took, the chain that held the boot up. Read-only.
	"analyze-time": {
		Paths:    []string{"/usr/bin/systemd-analyze"},
		Validate: exactly(Literal("time"), Literal("--no-pager")),
		Timeout:  15 * time.Second,
	},
	"analyze-blame": {
		Paths:    []string{"/usr/bin/systemd-analyze"},
		Validate: exactly(Literal("blame"), Literal("--no-pager")),
		Timeout:  15 * time.Second,
	},
	"analyze-chain": {
		Paths:    []string{"/usr/bin/systemd-analyze"},
		Validate: exactly(Literal("critical-chain"), Literal("--no-pager")),
		Timeout:  15 * time.Second,
	},
	// PipeWire (Settings page, audio): pw-dump lists the graph as JSON;
	// wpctl reads and sets one node's volume and mute, and picks the
	// default device. Node IDs are numbers from pw-dump.
	"pw-dump": {
		Paths:    []string{"/usr/bin/pw-dump"},
		Validate: exactly(Literal("--no-colors")),
		Env:      []string{"XDG_RUNTIME_DIR"},
		Timeout:  5 * time.Second,
	},
	"wpctl-get-volume": {
		Paths:    []string{"/usr/bin/wpctl"},
		Validate: exactly(Literal("get-volume"), PWNodeID),
		Env:      []string{"XDG_RUNTIME_DIR"},
		Timeout:  5 * time.Second,
	},
	"wpctl-set-volume": {
		Paths:    []string{"/usr/bin/wpctl"},
		Validate: exactly(Literal("set-volume"), PWNodeID, Volume),
		Env:      []string{"XDG_RUNTIME_DIR"},
		Timeout:  5 * time.Second,
	},
	"wpctl-set-mute": {
		Paths:    []string{"/usr/bin/wpctl"},
		Validate: exactly(Literal("set-mute"), PWNodeID, OneOf("0", "1")),
		Env:      []string{"XDG_RUNTIME_DIR"},
		Timeout:  5 * time.Second,
	},
	"wpctl-set-default": {
		Paths:    []string{"/usr/bin/wpctl"},
		Validate: exactly(Literal("set-default"), PWNodeID),
		Env:      []string{"XDG_RUNTIME_DIR"},
		Timeout:  5 * time.Second,
	},
}

var pwNodeIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

// PWNodeID accepts a PipeWire object ID.
func PWNodeID(s string) error {
	if !pwNodeIDRe.MatchString(s) {
		return invalid("not a PipeWire object ID: %q", s)
	}
	return nil
}

var volumeRe = regexp.MustCompile(`^[01]\.[0-9]{2}$`)

// Volume accepts a linear volume from "0.00" to "1.50" (wpctl allows
// boosting above 1.0; Hearth stops at 150%).
func Volume(s string) error {
	if !volumeRe.MatchString(s) || s > "1.50" {
		return invalid("not a volume from 0.00 to 1.50: %q", s)
	}
	return nil
}

// TrivyTemplate prints one vulnerability per line: ID, package,
// installed version, fixed version and severity, tab-separated. The tabs
// and newlines are template escapes: arguments carry no control characters.
const TrivyTemplate = `{{range .}}{{range .Vulnerabilities}}{{.VulnerabilityID}}{{"\t"}}{{.PkgName}}{{"\t"}}{{.InstalledVersion}}{{"\t"}}{{.FixedVersion}}{{"\t"}}{{.Severity}}{{"\n"}}{{end}}{{end}}`

var imageRefRe = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|[a-z0-9][a-z0-9._-]*(?::[0-9]{1,5})?(?:/[a-z0-9][a-z0-9._-]*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?)$`)

// debianSuiteRe matches a Debian release codename ("trixie", "sid").
var debianSuiteRe = regexp.MustCompile(`^[a-z]{2,16}$`)

// DebianSuite validates a Debian release codename.
func DebianSuite(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if !debianSuiteRe.MatchString(s) {
		return invalid("%q is not a Debian release codename", s)
	}
	return nil
}

// ImageRef validates an image ID or "name[:tag]" reference.
func ImageRef(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if len(s) > 255 || !imageRefRe.MatchString(s) || strings.Contains(s, "..") {
		return invalid("%q is not an image reference", s)
	}
	return nil
}

// TrivyCacheDir validates --cache-dir=<abs>/hearth/trivy.
func TrivyCacheDir(s string) error {
	dir, ok := strings.CutPrefix(s, "--cache-dir=")
	if !ok || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !strings.HasSuffix(dir, "/hearth/trivy") || strings.ContainsAny(dir, "\x00\n") {
		return invalid("%q is not Hearth's trivy cache", s)
	}
	return nil
}

// JobJournalFixedArgs start every journalctl-job command line.
var JobJournalFixedArgs = []string{"--quiet", "--no-pager", "--output=json",
	"--output-fields=MESSAGE,INVOCATION_ID,USER_INVOCATION_ID,_SYSTEMD_INVOCATION_ID,_TRANSPORT,EXIT_STATUS,JOB_RESULT", "--lines=1000"}

var jobBaseRe = regexp.MustCompile(`^hearth-job-[a-f0-9]{8}$`)

// JobJournalMatches selects a hearth-job's entries (base is its unit name
// without ".service"): the service's own, by cgroup; its manager's
// messages about it; and its output by SyslogIdentifier, which journald
// takes from the stream itself, because a job that exits at once can
// leave lines without unit or invocation fields. Every term pins _UID
// (_PID=1 for the system manager), so no other user can add lines with
// logger -t. A user job's entries are the running user's.
func JobJournalMatches(base string, user bool) []string {
	if user {
		uid := "_UID=" + strconv.Itoa(os.Getuid())
		return []string{"_SYSTEMD_USER_UNIT=" + base + ".service", uid, "+", "USER_UNIT=" + base + ".service", uid, "+", "SYSLOG_IDENTIFIER=" + base, uid}
	}
	return []string{"_SYSTEMD_UNIT=" + base + ".service", "_UID=0", "+", "UNIT=" + base + ".service", "_PID=1", "+", "SYSLOG_IDENTIFIER=" + base, "_UID=0"}
}

// jobJournalArgs accepts JobJournalFixedArgs, then JobJournalMatches of
// one hearth-job.
func jobJournalArgs(args []string) error {
	n := len(JobJournalFixedArgs)
	if len(args) <= n {
		return invalid("want more than %d arguments, got %d", n, len(args))
	}
	for i, want := range JobJournalFixedArgs {
		if err := Literal(want)(args[i]); err != nil {
			return err
		}
	}
	unit, user := strings.CutPrefix(args[n], "_SYSTEMD_USER_UNIT=")
	if !user {
		unit, _ = strings.CutPrefix(args[n], "_SYSTEMD_UNIT=")
	}
	base, ok := strings.CutSuffix(unit, ".service")
	if !ok || !jobBaseRe.MatchString(base) || !slices.Equal(args[n:], JobJournalMatches(base, user)) {
		return invalid("not a scheduled job's journal matches")
	}
	return nil
}

// JournalFixedArgs start every journalctl-unit command line.
var JournalFixedArgs = []string{"--quiet", "--no-pager", "--output=json", "--output-fields=MESSAGE,PRIORITY,_PID,SYSLOG_IDENTIFIER"}

// JournalQueryFixedArgs start every journalctl-query command line.
var JournalQueryFixedArgs = []string{"--quiet", "--no-pager", "--output=json", "--reverse", "--case-sensitive=no",
	"--output-fields=MESSAGE,PRIORITY,_PID,SYSLOG_IDENTIFIER,_SYSTEMD_UNIT,_SYSTEMD_USER_UNIT"}

// MaxJournalGrep bounds --grep= for journalctl-query.
const MaxJournalGrep = 512

var (
	journalTimeRe = regexp.MustCompile(`^@[0-9]{1,12}$`)
	journalBootRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// journalQueryArgs accepts JournalQueryFixedArgs, --lines=<n>, then each of
// these at most once, values always joined to their flag: --priority=<0-7>,
// --since=@<unix>, --until=@<unix>, --boot=<id>, --unit= or --user-unit=,
// --after-cursor=, --grep=<one line>.
func journalQueryArgs(args []string) error {
	n := len(JournalQueryFixedArgs)
	if len(args) <= n {
		return invalid("want more than %d arguments, got %d", n, len(args))
	}
	for i, want := range JournalQueryFixedArgs {
		if err := Literal(want)(args[i]); err != nil {
			return err
		}
	}
	lines, ok := strings.CutPrefix(args[n], "--lines=")
	if v, err := strconv.Atoi(lines); !ok || err != nil || v < 1 || v > MaxJournalLines || strconv.Itoa(v) != lines {
		return invalid("bad --lines argument %q", args[n])
	}
	seen := map[string]bool{}
	for _, a := range args[n+1:] {
		flag, value, ok := strings.Cut(a, "=")
		if !ok || seen[flag] {
			return invalid("bad or repeated argument %q", a)
		}
		seen[flag] = true
		var err error
		switch flag {
		case "--priority":
			err = OneOf("0", "1", "2", "3", "4", "5", "6", "7")(value)
		case "--since", "--until":
			if !journalTimeRe.MatchString(value) {
				err = invalid("bad time %q", value)
			}
		case "--boot":
			if !journalBootRe.MatchString(value) {
				err = invalid("bad boot %q", value)
			}
		case "--unit", "--user-unit":
			err = UnitName(value)
		case "--after-cursor":
			if !journalCursorRe.MatchString(value) {
				err = invalid("bad cursor")
			}
		case "--grep":
			err = journalGrep(value)
		default:
			err = invalid("unexpected argument %q", a)
		}
		if err != nil {
			return err
		}
	}
	if seen["--unit"] && seen["--user-unit"] {
		return invalid("one unit at a time")
	}
	if seen["--since"] && seen["--after-cursor"] {
		return invalid("journalctl refuses --since with a cursor")
	}
	return nil
}

func journalGrep(s string) error {
	if s == "" || len(s) > MaxJournalGrep || !utf8.ValidString(s) {
		return invalid("bad search text")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return invalid("bad search text")
		}
	}
	return nil
}

// MaxJournalLines bounds --lines= for journalctl-unit.
const MaxJournalLines = 1000

var journalCursorRe = regexp.MustCompile(`^[a-z]=[0-9a-f]{1,64}(;[a-z]=[0-9a-f]{1,64}){0,15}$`)

// journalArgs accepts JournalFixedArgs, then --lines=<1..MaxJournalLines>,
// then --unit=<unit> or --user-unit=<unit>, then optionally
// --after-cursor=<cursor>.
func journalArgs(args []string) error {
	n := len(JournalFixedArgs)
	if len(args) != n+2 && len(args) != n+3 {
		return invalid("want %d or %d arguments, got %d", n+2, n+3, len(args))
	}
	for i, want := range JournalFixedArgs {
		if err := Literal(want)(args[i]); err != nil {
			return err
		}
	}
	lines, ok := strings.CutPrefix(args[n], "--lines=")
	if v, err := strconv.Atoi(lines); !ok || err != nil || v < 1 || v > MaxJournalLines || strconv.Itoa(v) != lines {
		return invalid("bad --lines argument %q", args[n])
	}
	unit, ok := strings.CutPrefix(args[n+1], "--unit=")
	if !ok {
		unit, ok = strings.CutPrefix(args[n+1], "--user-unit=")
	}
	if !ok {
		return invalid("want --unit= or --user-unit=, got %q", args[n+1])
	}
	if err := UnitName(unit); err != nil {
		return err
	}
	if len(args) == n+3 {
		c, ok := strings.CutPrefix(args[n+2], "--after-cursor=")
		if !ok || !journalCursorRe.MatchString(c) {
			return invalid("bad --after-cursor argument")
		}
	}
	return nil
}

// exactly returns a Spec.Validate that requires one argument per validator,
// in order.
func exactly(vs ...Validator) func([]string) error {
	return func(args []string) error {
		if len(args) != len(vs) {
			return invalid("want %d arguments, got %d", len(vs), len(args))
		}
		for i, v := range vs {
			if err := v(args[i]); err != nil {
				return fmt.Errorf("argument %d: %w", i, err)
			}
		}
		return nil
	}
}
