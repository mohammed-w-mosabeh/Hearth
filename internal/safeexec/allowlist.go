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
)

// allowlist is the complete set of programs Hearth may run. Adding an entry
// needs a validator that accepts only the argument shapes the caller uses.
var allowlist = map[string]Spec{
	// xdg-open <http(s) URL>: open the UI (or a local service) in the browser.
	"xdg-open": {
		Paths:    []string{"/usr/bin/xdg-open"},
		Validate: exactly(HTTPURL),
		Env:      []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP", "HOME"},
	},
	// systemd-run --user --collect --quiet -- /usr/bin/xdg-open <URL>: open a
	// URL from inside hearthd.service without the browser inheriting the
	// service sandbox; the user manager starts it in a transient unit.
	"systemd-run-xdg-open": {
		Paths: []string{"/usr/bin/systemd-run"},
		Validate: exactly(
			Literal("--user"), Literal("--collect"), Literal("--quiet"), Literal("--"),
			Literal("/usr/bin/xdg-open"), HTTPURL,
		),
		Env: []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "HOME"},
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
}

// TrivyTemplate prints one vulnerability per line: ID, package,
// installed version, fixed version and severity, tab-separated. The tabs
// and newlines are template escapes: arguments carry no control characters.
const TrivyTemplate = `{{range .}}{{range .Vulnerabilities}}{{.VulnerabilityID}}{{"\t"}}{{.PkgName}}{{"\t"}}{{.InstalledVersion}}{{"\t"}}{{.FixedVersion}}{{"\t"}}{{.Severity}}{{"\n"}}{{end}}{{end}}`

var imageRefRe = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|[a-z0-9][a-z0-9._-]*(?::[0-9]{1,5})?(?:/[a-z0-9][a-z0-9._-]*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?)$`)

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
