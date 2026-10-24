package safeexec

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidArg is wrapped by every validator failure.
var ErrInvalidArg = errors.New("safeexec: invalid argument")

// Validator checks a single argument. It returns nil only for values that are
// safe to pass to the command at that position.
type Validator func(string) error

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArg, fmt.Sprintf(format, a...))
}

// baseCheck applies to every argument: valid UTF-8, no control characters
// (including NUL and newlines), bounded length.
func baseCheck(s string) error {
	if len(s) > 4096 {
		return invalid("argument too long (%d bytes)", len(s))
	}
	if !utf8.ValidString(s) {
		return invalid("argument is not valid UTF-8")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return invalid("argument contains control character %U", r)
		}
	}
	return nil
}

// noOption rejects values that a command could parse as an option (SECURITY S8).
func noOption(s string) error {
	if strings.HasPrefix(s, "-") {
		return invalid("%q looks like an option", s)
	}
	return nil
}

// Literal returns a validator that accepts exactly want. It is the only way
// a fixed option such as "--user" can appear in an argument list.
func Literal(want string) Validator {
	return func(s string) error {
		if s != want {
			return invalid("want %q, got %q", want, s)
		}
		return nil
	}
}

var debPackageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

// DebPackage validates a Debian package name (Debian Policy 5.6.1).
func DebPackage(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if !debPackageRe.MatchString(s) {
		return invalid("%q is not a Debian package name", s)
	}
	return nil
}

var unitSuffixes = []string{
	".service", ".socket", ".device", ".mount", ".automount", ".swap",
	".target", ".path", ".timer", ".slice", ".scope",
}

// UnitName validates a systemd unit name per systemd.unit(5): at most 255
// bytes, characters [A-Za-z0-9:_.\-@], a known type suffix, and at most one
// "@" separating template and instance. Names starting with "-" are rejected
// even though systemd allows "-.mount", because they would parse as options.
func UnitName(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if err := noOption(s); err != nil {
		return err
	}
	if len(s) > 255 {
		return invalid("unit name too long")
	}
	for _, r := range s {
		if !isUnitChar(r) {
			return invalid("unit name %q contains %q", s, r)
		}
	}
	var suffix string
	for _, suf := range unitSuffixes {
		if strings.HasSuffix(s, suf) {
			suffix = suf
			break
		}
	}
	if suffix == "" {
		return invalid("unit name %q has no valid type suffix", s)
	}
	prefix := strings.TrimSuffix(s, suffix)
	if prefix == "" {
		return invalid("unit name %q has an empty prefix", s)
	}
	if n := strings.Count(prefix, "@"); n > 1 {
		return invalid("unit name %q has more than one @", s)
	} else if n == 1 && strings.HasPrefix(prefix, "@") {
		return invalid("unit name %q has an empty template prefix", s)
	}
	return nil
}

func isUnitChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune(`:_.\-@`, r)
}

var containerIDRe = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// ContainerID validates a short or full Docker container/image ID.
func ContainerID(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if !containerIDRe.MatchString(s) {
		return invalid("%q is not a container ID", s)
	}
	return nil
}

// PathUnder returns a validator that accepts absolute, already-clean paths
// equal to or inside one of roots. Roots must themselves be absolute and clean.
// Symlinks are not resolved here; callers that open the path must do so.
func PathUnder(roots ...string) Validator {
	for _, r := range roots {
		if !filepath.IsAbs(r) || filepath.Clean(r) != r {
			panic(fmt.Sprintf("safeexec: root %q must be absolute and clean", r))
		}
	}
	return func(s string) error {
		if err := baseCheck(s); err != nil {
			return err
		}
		if !filepath.IsAbs(s) {
			return invalid("path %q is not absolute", s)
		}
		if filepath.Clean(s) != s {
			return invalid("path %q is not clean", s)
		}
		for _, r := range roots {
			if s == r || strings.HasPrefix(s, r+"/") || r == "/" {
				return nil
			}
		}
		return invalid("path %q is outside allowed roots", s)
	}
}

// HTTPURL validates an absolute http(s) URL with a host and no credentials.
func HTTPURL(s string) error {
	if err := baseCheck(s); err != nil {
		return err
	}
	if err := noOption(s); err != nil {
		return err
	}
	if strings.ContainsAny(s, " \\") {
		return invalid("URL %q contains a space or backslash", s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return invalid("URL %q does not parse", s)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return invalid("URL %q is not http(s)", s)
	}
	if u.Host == "" || u.Hostname() == "" {
		return invalid("URL %q has no host", s)
	}
	if u.User != nil {
		return invalid("URL %q contains credentials", s)
	}
	if u.Opaque != "" {
		return invalid("URL %q is opaque", s)
	}
	return nil
}
