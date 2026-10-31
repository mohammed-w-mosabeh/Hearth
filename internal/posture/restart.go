package posture

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/space"
)

const (
	restartEvery   = time.Hour
	restartTimeout = 3 * time.Minute
	maxRebootPkgs  = 50
)

// RestartScan is what needrestart found (through the broker).
type RestartScan struct {
	Installed bool
	Services  []string
	Sessions  []string
}

// Restart says what an update left waiting.
type Restart struct {
	// Reboot: Debian's flag or a newer kernel.
	Reboot   bool     `json:"reboot"`
	Flagged  bool     `json:"flagged"`            // /run/reboot-required
	Packages []string `json:"packages,omitempty"` // that asked for it
	Running  string   `json:"running,omitempty"`
	Newest   string   `json:"newest,omitempty"` // set when newer than Running

	Scanned   bool     `json:"scanned"`         // needrestart has answered
	Installed bool     `json:"installed"`       // needrestart is there
	Services  []string `json:"services"`        // running old libraries
	Sessions  []string `json:"sessions"`        //
	Time      int64    `json:"time,omitempty"`  // of that answer, unix ms
	Error     string   `json:"error,omitempty"` // the last scan failed
}

type restartCache struct {
	mu      sync.Mutex
	at      time.Time
	scan    RestartScan
	err     string
	running chan struct{}
}

// kernelBuild strips a Debian release down to its version, leaving the
// flavor: 6.12.48+deb13-rt-amd64 is rt-amd64, 6.1.0-18-amd64 is amd64.
var kernelBuild = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*(\+deb[0-9a-z.+]*)?(-[0-9]+)?-?`)

func kernelFlavor(release string) string { return kernelBuild.ReplaceAllString(release, "") }

// newestKernel is the newest installed kernel of running's flavor, read
// from /boot, or "" when running is it. Another flavor is not an upgrade.
func newestKernel(root, running string) string {
	if running == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(root, "/boot", "vmlinuz-*"))
	best := running
	for _, f := range files {
		r := strings.TrimPrefix(filepath.Base(f), "vmlinuz-")
		if kernelFlavor(r) == kernelFlavor(running) && space.CompareKernels(r, best) > 0 {
			best = r
		}
	}
	if best == running {
		return ""
	}
	return best
}

func rebootPackages(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, "/run", "reboot-required.pkgs")) // #nosec G304 -- a fixed path
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Fields(string(b)) {
		if !seen[l] && len(out) < maxRebootPkgs {
			seen[l] = true
			out = append(out, oneLine(l, 80))
		}
	}
	sort.Strings(out)
	return out
}

// refreshRestart starts a scan unless one is running or recent. The channel
// closes when the answer is in.
func (s *Scanner) refreshRestart() <-chan struct{} {
	c := &s.restart
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		return c.running
	}
	done := make(chan struct{})
	if s.cfg.Needrestart == nil || (!c.at.IsZero() && s.cfg.Clock().Sub(c.at) < restartEvery) {
		close(done)
		return done
	}
	c.running = done
	go func() {
		// Not the request's context: a closed tab must not cost the answer.
		ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
		defer cancel()
		scan, err := s.cfg.Needrestart(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.at, c.running = s.cfg.Clock(), nil
		if err != nil {
			c.err = err.Error() // the last good answer stands, marked
		} else {
			c.scan, c.err = scan, ""
		}
		close(done)
	}()
	return done
}

// Restart reports what waits for a restart. The files are read now; the
// needrestart answer is the last one, refreshed in the background. With
// wait, it waits for that scan until ctx ends.
func (s *Scanner) Restart(ctx context.Context, wait bool) Restart {
	out := Restart{Services: []string{}, Sessions: []string{}}
	out.Flagged = exists(filepath.Join(s.cfg.Root, "/run", "reboot-required"))
	if out.Flagged {
		out.Packages = rebootPackages(s.cfg.Root)
	}
	if s.cfg.Release != nil {
		out.Running = s.cfg.Release()
		out.Newest = newestKernel(s.cfg.Root, out.Running)
	}
	out.Reboot = out.Flagged || out.Newest != ""
	done := s.refreshRestart()
	if wait {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	c := &s.restart
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() {
		out.Scanned, out.Installed, out.Time, out.Error = true, c.scan.Installed, c.at.UnixMilli(), c.err
		out.Services = append(out.Services, c.scan.Services...)
		out.Sessions = append(out.Sessions, c.scan.Sessions...)
		sort.Strings(out.Services)
	}
	return out
}

func restartCheck(r Restart) Check {
	c := Check{ID: CheckRestart}
	if r.Flagged {
		c.Findings = append(c.Findings, Finding{Code: "restart.reboot", Severity: SevWarn, Details: r.Packages})
	}
	if r.Newest != "" {
		c.Findings = append(c.Findings, Finding{Code: "restart.kernel", Severity: SevWarn, Subject: r.Running + " → " + r.Newest})
	}
	if len(r.Services) > 0 {
		c.Findings = append(c.Findings, Finding{Code: "restart.services", Severity: SevWarn, Subject: itoa(len(r.Services)), Details: firstN(r.Services, 50)})
	}
	if len(r.Sessions) > 0 {
		c.Findings = append(c.Findings, Finding{Code: "restart.sessions", Severity: SevInfo, Subject: itoa(len(r.Sessions)), Details: firstN(r.Sessions, 20)})
	}
	switch {
	case r.Error != "":
		c.Findings = append(c.Findings, Finding{Code: "restart.scan_failed", Severity: SevInfo})
	case !r.Scanned:
		c.Findings = append(c.Findings, Finding{Code: "restart.scanning", Severity: SevInfo})
	case !r.Installed:
		c.Findings = append(c.Findings, Finding{Code: "restart.no_tool", Severity: SevInfo})
	}
	return finish(c)
}
