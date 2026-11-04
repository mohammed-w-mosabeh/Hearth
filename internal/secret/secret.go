// Package secret is SECURITY S19 in one place: which names and values are
// masked wherever Hearth shows an environment, in hearthd and in the broker.
package secret

import (
	"net/url"
	"regexp"
	"strings"
)

// Mask replaces a masked value.
const Mask = "********"

var (
	// word is S19's list; "pass" covers PASSWORD and PASSWD.
	word = regexp.MustCompile(`(?i)pass|pwd|token|secret|key|auth|cred`)
	// suffix: from v1.5, names ending so usually hold a connection string.
	suffix = regexp.MustCompile(`(?i)_(URL|URI|DSN)$`)
	// userinfo finds "scheme://user:pass@" or "scheme://user@" anywhere in a
	// text, without parsing it: a value need not be a well-formed URL to leak.
	userinfo = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s?#@]+@`)
)

// Name reports whether a variable named k has its value masked.
func Name(k string) bool { return word.MatchString(k) || suffix.MatchString(k) }

// Value reports whether v holds a URL with user info, whatever its name.
func Value(v string) bool {
	if userinfo.MatchString(v) {
		return true
	}
	// A URL the regexp missed but net/url reads as carrying a user.
	u, err := url.Parse(strings.TrimSpace(v))
	return err == nil && u.User != nil
}

// Env returns env ("KEY=value" entries) with the values S19 masks replaced
// by Mask. An entry without "=" is kept as it is.
func Env(env []string) []string {
	out := make([]string, len(env))
	for i, e := range env {
		k, v, found := strings.Cut(e, "=")
		if found && (Name(k) || Value(v)) {
			e = k + "=" + Mask
		}
		out[i] = e
	}
	return out
}

// Line reports whether a line of text names a secret or holds a URL with
// user info. Words inside long base64 runs are not read: an SSH public key
// holds "key" by chance, and that is a line to show. A name beside such a
// run (API_KEY=...) still counts.
func Line(s string) bool {
	return word.MatchString(longBlob.ReplaceAllString(s, "")) || suffixInLine.MatchString(s) || userinfo.MatchString(s)
}

var (
	longBlob = regexp.MustCompile(`[A-Za-z0-9+/]{64,}={0,2}`)
	// A name ending _URL, _URI or _DSN being assigned: "DB_URL=", "DB_DSN: ".
	suffixInLine = regexp.MustCompile(`(?i)_(URL|URI|DSN)\s*[=:]`)
)
