package launch

import (
	"errors"
	"fmt"
	"strings"
)

// maxArgs bounds an Exec line; real ones have a handful.
const maxArgs = 64

// fields are the values the field codes of an Exec line expand to.
type fields struct {
	Name, Icon, Path string // %c, %i, %k
}

// parseExec splits a raw Exec value (Desktop Entry Specification 1.5,
// "The Exec key") into argv and expands its field codes. Hearth passes no
// files or URLs, so %f %F %u %U expand to nothing; the deprecated codes
// are removed as the spec asks. Nothing is ever given to a shell: quoting
// only groups arguments.
func parseExec(raw string, f fields) ([]string, error) {
	words, err := splitExec(unescapeString(raw))
	if err != nil {
		return nil, err
	}
	var argv []string
	for _, w := range words {
		if !w.hasCode {
			argv = append(argv, w.text)
			continue
		}
		// A field code standing alone may expand to zero or two arguments.
		switch w.text {
		case "%f", "%F", "%u", "%U":
			continue
		case "%i":
			if f.Icon != "" {
				argv = append(argv, "--icon", f.Icon)
			}
			continue
		}
		s, err := expand(w.text, f)
		if err != nil {
			return nil, err
		}
		argv = append(argv, s)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("the Exec line names no program")
	}
	if len(argv) > maxArgs {
		return nil, fmt.Errorf("the Exec line has more than %d arguments", maxArgs)
	}
	return argv, nil
}

// expand replaces the field codes inside one unquoted argument.
func expand(s string, f fields) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) {
			return "", errors.New("the Exec line ends in a lone %")
		}
		i++
		switch s[i] {
		case '%':
			b.WriteByte('%')
		case 'c':
			b.WriteString(f.Name)
		case 'k':
			b.WriteString(f.Path)
		case 'f', 'F', 'u', 'U', 'i', 'd', 'D', 'n', 'N', 'v', 'm':
			// no files to pass; %i inside a word has no defined meaning
		default:
			return "", fmt.Errorf("the Exec line has an unknown field code %%%c", s[i])
		}
	}
	return b.String(), nil
}

type word struct {
	text    string
	hasCode bool // contains field codes still to expand
}

// splitExec splits on unquoted spaces and tabs. Inside double quotes a
// backslash escapes ", `, $ and \, and field codes are not expanded
// (the spec forbids them there; they are kept literally). Outside quotes
// a backslash escapes the next character, as GLib does.
func splitExec(s string) ([]word, error) {
	var out []word
	var cur strings.Builder
	inWord, code := false, false
	flush := func() {
		if inWord {
			out = append(out, word{text: cur.String(), hasCode: code})
		}
		cur.Reset()
		inWord, code = false, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '"':
			inWord = true
			closed := false
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"`$\\", s[i+1]) >= 0 {
					i++
				}
				if s[i] == '%' {
					cur.WriteString("%%") // literal: survives expand
					continue
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errors.New("the Exec line has an unterminated quote")
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			if s[i] == '%' {
				cur.WriteString("%%")
			} else {
				cur.WriteByte(s[i])
			}
		case c == '%':
			inWord, code = true, true
			cur.WriteByte(c)
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	flush()
	// Words with a "%%" from quoting but no field code still need it undone.
	for i := range out {
		if !out[i].hasCode && strings.Contains(out[i].text, "%%") {
			out[i].text = strings.ReplaceAll(out[i].text, "%%", "%")
		}
	}
	return out, nil
}

// unescapeString applies the spec's string escapes (\s \n \t \r \\) that
// every value has before Exec quoting is interpreted. Other backslashes
// are kept for the quoting rules.
func unescapeString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}
