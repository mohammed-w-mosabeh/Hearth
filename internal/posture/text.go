package posture

import "strings"

// checkNames and texts are the English strings for exports (reports);
// the UI has its own in en.json ("security.check.<id>", "security.f.<code>").
var checkNames = map[string]string{
	CheckListeners:  "Listeners on every address",
	CheckFirewall:   "Firewall",
	CheckSSH:        "SSH server",
	CheckUpdates:    "Security updates",
	CheckCVEs:       "Known vulnerabilities (debsecan)",
	CheckContainers: "Containers",
	CheckPath:       "Programs in PATH",
}

var texts = map[string]string{
	"firewall.active":         "Active: {s}",
	"firewall.inactive":       "Installed but not active: {s}",
	"firewall.none":           "No firewall is active",
	"listener.exposed":        "{s} listens on every address",
	"ssh.active":              "Running, port {s}",
	"ssh.inactive":            "Installed, not running",
	"ssh.root_login":          "root may log in with a password (PermitRootLogin {s})",
	"ssh.empty_passwords":     "Accounts with empty passwords may log in",
	"ssh.password_auth":       "Password logins are allowed",
	"ssh.kbd_interactive":     "Keyboard-interactive logins are allowed",
	"ssh.unreadable":          "Some configuration files could not be read",
	"ssh.match":               "Match blocks change these settings for some clients",
	"updates.security":        "{s} security updates are waiting",
	"updates.other":           "{s} other updates are waiting",
	"cves.not_installed":      "debsecan is not installed",
	"cves.not_run":            "Not checked yet",
	"cves.fixed":              "{s} vulnerabilities have a fix available",
	"cves.open":               "{s} vulnerabilities have no fix yet",
	"container.risky":         "{s} could take over the host",
	"container.risky_stopped": "{s} (stopped) could take over the host when started",
	"path.world":              "{s} is writable by anyone",
	"path.world_file":         "{s} is writable by anyone",
	"path.owner":              "{s} belongs to another user",
	"path.owner_file":         "{s} belongs to another user",
	"path.relative":           "PATH has the relative entry {s}",
	"path.truncated":          "Only the first {s} are listed",
}

// CheckName is a check's English name.
func CheckName(id string) string {
	if n, ok := checkNames[id]; ok {
		return n
	}
	return id
}

// Text is a finding in English, with its details after a colon.
func Text(f Finding) string {
	t, ok := texts[f.Code]
	if !ok {
		t = f.Code + " {s}"
	}
	t = strings.TrimSpace(strings.ReplaceAll(t, "{s}", f.Subject))
	if len(f.Details) > 0 {
		t += ": " + strings.Join(f.Details, ", ")
	}
	return t
}
