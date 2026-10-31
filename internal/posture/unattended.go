package posture

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	unattendedTimer = "apt-daily-upgrade.timer"
	unattendedStale = 7 * 24 * time.Hour
)

// parseAptShell reads `apt-config shell U <key> L <key>`: lines like U='1'.
// A key that is not set prints nothing.
func parseAptShell(b []byte) (upgrade, lists string) {
	for _, l := range strings.Split(string(b), "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `'"`)
		switch name {
		case "U":
			upgrade = val
		case "L":
			lists = val
		}
	}
	return upgrade, lists
}

// periodicDays reads an APT::Periodic value: a number of days, "always" (0
// days apart), or 0, unset and anything else for off.
func periodicDays(v string) (days int, on bool) {
	if strings.EqualFold(v, "always") {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// unattended reports automatic updates, read-only: the settings through
// apt-config, whose `shell` form returns only the keys asked for (never
// proxy credentials), and the last run from apt's stamp, since the log is
// not readable by the user.
func (s *Scanner) unattended(ctx context.Context) Check {
	c := Check{ID: CheckUnattended}
	if !exists(filepath.Join(s.cfg.Root, "/usr/bin/unattended-upgrade")) {
		c.Findings = append(c.Findings, Finding{Code: "unattended.no_tool", Severity: SevInfo})
		return finish(c)
	}
	if s.cfg.Run == nil {
		return unknown(CheckUnattended, nil)
	}
	res, err := s.cfg.Run(ctx, "apt-config-periodic", "shell", "U", "APT::Periodic::Unattended-Upgrade", "L", "APT::Periodic::Update-Package-Lists")
	if err != nil {
		return unknown(CheckUnattended, err)
	}
	upgrade, lists := parseAptShell(res.Stdout)
	days, on := periodicDays(upgrade)
	if !on {
		c.Findings = append(c.Findings, Finding{Code: "unattended.off", Severity: SevWarn})
		return finish(c)
	}
	if _, ok := periodicDays(lists); !ok {
		c.Findings = append(c.Findings, Finding{Code: "unattended.no_lists", Severity: SevWarn})
	}
	if s.cfg.UnitActive != nil && !s.cfg.UnitActive(ctx, unattendedTimer) {
		c.Findings = append(c.Findings, Finding{Code: "unattended.no_timer", Severity: SevWarn, Subject: unattendedTimer})
	}
	fi, err := os.Stat(filepath.Join(s.cfg.Root, "/var/lib/apt/periodic/unattended-upgrades-stamp"))
	if err != nil {
		c.Findings = append(c.Findings, Finding{Code: "unattended.never", Severity: SevInfo})
		return finish(c)
	}
	// A long interval is the owner's choice, not a stalled run.
	limit := max(unattendedStale, time.Duration(days+1)*24*time.Hour)
	date := fi.ModTime().Format(time.DateOnly)
	if s.cfg.Clock().Sub(fi.ModTime()) > limit {
		c.Findings = append(c.Findings, Finding{Code: "unattended.stale", Severity: SevWarn, Subject: date})
	} else {
		c.Findings = append(c.Findings, Finding{Code: "unattended.active", Severity: SevInfo, Subject: date})
	}
	return finish(c)
}
