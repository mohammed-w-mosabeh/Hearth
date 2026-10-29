package posture

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// CVE is one vulnerability debsecan reports for an installed package.
type CVE struct {
	ID      string `json:"id"` // CVE-2024-1234, or a Debian TEMP- ID
	Package string `json:"package"`
	Fixed   bool   `json:"fixed"`  // a fixed version is available
	Remote  bool   `json:"remote"` // remotely exploitable
	Urgency string `json:"urgency,omitempty"`
}

// CVEs is a debsecan run.
type CVEs struct {
	Installed bool   `json:"installed"`
	Suite     string `json:"suite,omitempty"`
	Time      int64  `json:"time,omitempty"` // unix ms of the run
	Error     string `json:"error,omitempty"`
	List      []CVE  `json:"list"`
	Fixed     int    `json:"fixed"`
	Remote    int    `json:"remote"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// maxCVEs bounds the CVEs kept from one run.
const maxCVEs = 5000

// summaryRe matches a line of `debsecan --format summary`:
// "CVE-2024-1234 libfoo (fixed, remotely exploitable, high urgency)".
var summaryRe = regexp.MustCompile(`^([A-Za-z0-9-]{1,64}) ([a-z0-9][a-z0-9+.-]{0,127})(?: \(([^()]*)\))?$`)

// parseSummary reads debsecan's summary; lines it does not recognise are
// skipped.
func parseSummary(b []byte) (list []CVE, truncated bool) {
	list = []CVE{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		m := summaryRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		if len(list) >= maxCVEs {
			return list, true
		}
		c := CVE{ID: m[1], Package: m[2]}
		for _, note := range strings.Split(m[3], ",") {
			note = strings.TrimSpace(note)
			switch {
			case note == "fixed":
				c.Fixed = true
			case note == "remotely exploitable":
				c.Remote = true
			case strings.HasSuffix(note, " urgency"):
				c.Urgency = strings.TrimSuffix(note, " urgency")
			}
		}
		list = append(list, c)
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Fixed != b.Fixed {
			return a.Fixed
		}
		if a.Remote != b.Remote {
			return a.Remote
		}
		return urgencyRank(a.Urgency) > urgencyRank(b.Urgency)
	})
	return list, false
}

func urgencyRank(u string) int {
	switch u {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

// codename is VERSION_CODENAME from os-release.
func codename(root string) string {
	b, err := os.ReadFile(root + "/etc/os-release") // #nosec G304 -- a fixed path under the test root
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_CODENAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// runDebsecan runs debsecan for this release.
func runDebsecan(ctx context.Context, run Runner, root string) CVEs {
	out := CVEs{Installed: exists(root + "/usr/bin/debsecan"), List: []CVE{}}
	if !out.Installed {
		return out
	}
	out.Suite = codename(root)
	if out.Suite == "" {
		out.Error = "the Debian release is unknown (no VERSION_CODENAME in /etc/os-release)"
		return out
	}
	res, err := run(ctx, "debsecan", "--suite", out.Suite, "--format", "summary")
	if err != nil {
		msg := strings.TrimSpace(string(res.Stderr))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:] // a Python traceback ends with the error
		}
		if msg == "" || errors.Is(err, context.DeadlineExceeded) {
			msg = err.Error()
		}
		out.Error = oneLine(msg, 300)
		return out
	}
	out.List, out.Truncated = parseSummary(res.Stdout)
	out.Truncated = out.Truncated || res.Truncated
	for _, c := range out.List {
		if c.Fixed {
			out.Fixed++
		}
		if c.Remote {
			out.Remote++
		}
	}
	return out
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
