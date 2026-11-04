package integrity

import (
	"strings"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
)

const (
	// Hidden replaces a line that looks like it holds a secret.
	Hidden = "[hidden]"

	maxLineRunes = 400
	maxDiffLines = 400
	maxLCSCells  = 1_000_000 // past this a change is shown as replaced, whole
	context3     = 3
)

// maskLine hides a line that names a secret or holds a URL with user info
// (SECURITY S19).
func maskLine(s string) string {
	if secret.Line(s) {
		return Hidden
	}
	return s
}

// maskText masks a whole file, line by line, before it is stored or shown.
func maskText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = maskLine(l)
	}
	return strings.Join(lines, "\n")
}

// cleanLine keeps a line printable and short. Tabs stay.
func cleanLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if r := []rune(s); len(r) > maxLineRunes {
		s = string(r[:maxLineRunes])
	}
	return s
}

// Line is one line of a diff. Op is "+", "-" or " "; Gap marks skipped lines.
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text"`
	Gap  bool   `json:"gap,omitempty"`
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// edits lists the operations turning a into b.
func edits(a, b []string) []Line {
	n, m := len(a), len(b)
	out := make([]Line, 0, n+m)
	pre := 0
	for pre < n && pre < m && a[pre] == b[pre] {
		out = append(out, Line{Op: " ", Text: a[pre]})
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	x, y := a[pre:n-suf], b[pre:m-suf]
	if len(x)*len(y) > maxLCSCells {
		for _, l := range x {
			out = append(out, Line{Op: "-", Text: l})
		}
		for _, l := range y {
			out = append(out, Line{Op: "+", Text: l})
		}
	} else {
		w := len(y) + 1
		t := make([]uint32, (len(x)+1)*w)
		for i := len(x) - 1; i >= 0; i-- {
			for j := len(y) - 1; j >= 0; j-- {
				if x[i] == y[j] {
					t[i*w+j] = t[(i+1)*w+j+1] + 1
				} else {
					t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(x) || j < len(y) {
			switch {
			case i < len(x) && j < len(y) && x[i] == y[j]:
				out = append(out, Line{Op: " ", Text: x[i]})
				i, j = i+1, j+1
			case j == len(y) || (i < len(x) && t[(i+1)*w+j] >= t[i*w+j+1]):
				out = append(out, Line{Op: "-", Text: x[i]})
				i++
			default:
				out = append(out, Line{Op: "+", Text: y[j]})
				j++
			}
		}
	}
	for _, l := range a[n-suf:] {
		out = append(out, Line{Op: " ", Text: l})
	}
	return out
}

// Diff compares two masked texts, keeping three lines around each change.
// visible is false when no changed line can be shown: only hidden lines
// differ, which the caller must say rather than show an empty diff.
func Diff(before, after string) (lines []Line, truncated, visible bool) {
	all := edits(splitLines(before), splitLines(after))
	keep := make([]bool, len(all))
	for i, l := range all {
		if l.Op == " " {
			continue
		}
		if l.Text != Hidden {
			visible = true
		}
		for k := max(0, i-context3); k <= min(len(all)-1, i+context3); k++ {
			keep[k] = true
		}
	}
	lines = []Line{}
	for i, l := range all {
		if !keep[i] {
			if i > 0 && keep[i-1] && i < len(all) {
				lines = append(lines, Line{Op: " ", Gap: true})
			}
			continue
		}
		if len(lines) >= maxDiffLines {
			return lines, true, visible
		}
		l.Text = cleanLine(l.Text)
		lines = append(lines, l)
	}
	return lines, false, visible
}
