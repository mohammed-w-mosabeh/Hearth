package posture

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SSH is what the OpenSSH server's configuration allows.
type SSH struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	// Effective values (sshd's defaults where nothing sets them), lower case.
	PermitRootLogin   string   `json:"permit_root_login"`
	PasswordAuth      string   `json:"password_authentication"`
	KbdInteractive    string   `json:"kbd_interactive_authentication"`
	PermitEmptyPasswd string   `json:"permit_empty_passwords"`
	Ports             []string `json:"ports"`
	ListenAddresses   []string `json:"listen_addresses"`
	// Unreadable lists configuration files Hearth could not read (some
	// are root-only), so the values may differ from sshd's.
	Unreadable []string `json:"unreadable,omitempty"`
	// Match is set when Match blocks change some values for some clients.
	Match bool `json:"match,omitempty"`
}

// sshd's built-in defaults (sshd_config(5), OpenSSH 9/10).
var sshDefaults = map[string]string{
	"permitrootlogin":              "prohibit-password",
	"passwordauthentication":       "yes",
	"kbdinteractiveauthentication": "yes",
	"permitemptypasswords":         "no",
}

// maxIncludes bounds files read through Include.
const maxIncludes = 64

// readSSH reads root+/etc/ssh/sshd_config as sshd does: the first value
// of an option wins, Include is expanded in place (relative paths are
// under /etc/ssh), and the global section ends at the first Match.
func readSSH(root string) SSH {
	st := SSH{Installed: exists(root + "/usr/sbin/sshd")}
	seen := map[string]string{}
	var ports, addrs []string
	inMatch := false
	files := 0
	var read func(path string, depth int)
	read = func(path string, depth int) {
		files++
		b, err := os.ReadFile(path) // #nosec G304 G703 -- sshd's own configuration under a fixed root
		if err != nil {
			if !os.IsNotExist(err) {
				st.Unreadable = append(st.Unreadable, strings.TrimPrefix(path, root))
			}
			return
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() && !inMatch {
			key, val := sshLine(sc.Text())
			switch key {
			case "":
				continue
			case "match":
				inMatch, st.Match = true, true
				return
			case "include":
				if depth >= 4 {
					continue
				}
				for _, pat := range strings.Fields(val) {
					if !strings.HasPrefix(pat, "/") {
						pat = "/etc/ssh/" + pat
					}
					matches, _ := filepath.Glob(root + filepath.Clean(pat))
					sort.Strings(matches)
					for _, m := range matches {
						if files < maxIncludes && !inMatch {
							read(m, depth+1)
						}
					}
				}
			case "port":
				ports = append(ports, val)
			case "listenaddress":
				addrs = append(addrs, val)
			default:
				if _, ok := seen[key]; !ok {
					seen[key] = strings.ToLower(val)
				}
			}
		}
	}
	read(root+"/etc/ssh/sshd_config", 0)
	val := func(k string) string {
		if v, ok := seen[k]; ok {
			return v
		}
		return sshDefaults[k]
	}
	st.PermitRootLogin = val("permitrootlogin")
	st.PasswordAuth = val("passwordauthentication")
	// ChallengeResponseAuthentication is the older name.
	if _, ok := seen["kbdinteractiveauthentication"]; !ok {
		if v, ok := seen["challengeresponseauthentication"]; ok {
			seen["kbdinteractiveauthentication"] = v
		}
	}
	st.KbdInteractive = val("kbdinteractiveauthentication")
	st.PermitEmptyPasswd = val("permitemptypasswords")
	if len(ports) == 0 {
		ports = []string{"22"}
	}
	st.Ports, st.ListenAddresses = ports, addrs
	if st.ListenAddresses == nil {
		st.ListenAddresses = []string{}
	}
	return st
}

// sshLine splits a configuration line into a lower-case keyword and its
// value ("Key value", "Key=value", comments and blank lines give "").
func sshLine(line string) (key, val string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", ""
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), ""
	}
	key = strings.ToLower(line[:i])
	val = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line[i:]), "="))
	val = strings.Trim(val, `"`)
	return key, val
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
