// Package secrets reads and writes a stack's secrets.env (ROADMAP 9.5,
// SECURITY S12c), with no I/O. Compose reads the file through its fixed
// --env-file to resolve the ${KEY} references the canonical compose file
// holds, so each value is written double-quoted with \, " and $ escaped:
// Compose then interpolates nothing in it and gives back exactly the bytes
// set (checked against Compose v5.6). A value is one line: newlines and
// other control characters but tab are refused.
package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxValue bounds one value in bytes.
	MaxValue = 8 << 10
	// MaxTotal bounds the file in bytes, so a set fits a broker line.
	MaxTotal = 32 << 10
	// MaxKeys bounds how many keys a stack holds.
	MaxKeys = 64
)

var key = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidKey reports whether k can name a secret: a variable name that is not
// a Compose or Docker setting, which Compose would read from the file.
func ValidKey(k string) bool {
	u := strings.ToUpper(k)
	return key.MatchString(k) && !strings.HasPrefix(u, "COMPOSE_") && !strings.HasPrefix(u, "DOCKER_") && u != "BUILDKIT_PROGRESS"
}

// CheckValue says why v cannot be a secret value, or "".
func CheckValue(v string) string {
	switch {
	case len(v) > MaxValue:
		return fmt.Sprintf("a value is at most %d bytes", MaxValue)
	case !utf8.ValidString(v):
		return "a value must be UTF-8"
	case strings.ContainsFunc(v, func(r rune) bool { return (r < 0x20 && r != '\t') || r == 0x7f }):
		return "a value is one line, without control characters"
	}
	return ""
}

// Encode renders values as the file Compose reads, one sorted line per key.
// It refuses an invalid key or value; the error never holds a value.
func Encode(values map[string]string) ([]byte, error) {
	if len(values) > MaxKeys {
		return nil, fmt.Errorf("at most %d secrets", MaxKeys)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		if !ValidKey(k) {
			return nil, fmt.Errorf("%q cannot name a secret", clip(k))
		}
		if why := CheckValue(values[k]); why != "" {
			return nil, fmt.Errorf("%s: %s", k, why)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escaper.Replace(values[k]))
		b.WriteString("\"\n")
	}
	if b.Len() > MaxTotal {
		return nil, fmt.Errorf("the secrets are at most %d bytes in all", MaxTotal)
	}
	return []byte(b.String()), nil
}

var escaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)

// Parse reads a file Encode wrote, and nothing else: one KEY="value" line
// each, escapes only as Encode makes them. An empty file has no secrets.
func Parse(b []byte) (map[string]string, error) {
	if len(b) > MaxTotal {
		return nil, errors.New("secrets.env is too large")
	}
	out := map[string]string{}
	s := string(b)
	if s == "" {
		return out, nil
	}
	if !strings.HasSuffix(s, "\n") {
		return nil, errors.New("secrets.env does not end with a newline")
	}
	for n, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		k, rest, ok := strings.Cut(line, `="`)
		if !ok || !ValidKey(k) || !strings.HasSuffix(rest, `"`) || len(rest) < 1 {
			return nil, fmt.Errorf("secrets.env line %d is not KEY=\"value\"", n+1)
		}
		v, err := unescape(rest[:len(rest)-1])
		if err != nil {
			return nil, fmt.Errorf("secrets.env line %d: %w", n+1, err)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("secrets.env names %s twice", k)
		}
		if why := CheckValue(v); why != "" {
			return nil, fmt.Errorf("secrets.env line %d: %s", n+1, why)
		}
		out[k] = v
	}
	if len(out) > MaxKeys {
		return nil, fmt.Errorf("secrets.env holds more than %d secrets", MaxKeys)
	}
	return out, nil
}

func unescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			if i+1 == len(s) {
				return "", errors.New("a lone backslash")
			}
			i++
			switch s[i] {
			case '\\', '"', '$':
				b.WriteByte(s[i])
			default:
				return "", errors.New("an unknown escape")
			}
		case '"', '$':
			return "", errors.New("an unescaped quote or dollar")
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

func clip(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(s, "?")
}
