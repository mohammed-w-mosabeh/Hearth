package discovery

import (
	"errors"
	"strings"
)

// vdf is a node of Valve's text KeyValues format (libraryfolders.vdf,
// appmanifest_*.acf, localconfig.vdf): a value, or children in file order.
type vdf struct {
	Value string
	Kids  []vdfKV
}

type vdfKV struct {
	Key  string
	Node *vdf
}

// maxVDFDepth bounds nesting; real files go five or six levels deep.
const maxVDFDepth = 32

// parseVDF parses a KeyValues text file into its root node.
func parseVDF(s string) (*vdf, error) {
	p := vdfParser{s: s}
	root := &vdf{}
	if err := p.object(root, 0, false); err != nil {
		return nil, err
	}
	return root, nil
}

// Get follows keys (case-insensitive, as Steam reads them); nil if missing.
func (n *vdf) Get(keys ...string) *vdf {
	for _, k := range keys {
		if n == nil {
			return nil
		}
		var next *vdf
		for _, kv := range n.Kids {
			if strings.EqualFold(kv.Key, k) {
				next = kv.Node
				break
			}
		}
		n = next
	}
	return n
}

// Str is the value at keys, or "".
func (n *vdf) Str(keys ...string) string {
	if v := n.Get(keys...); v != nil {
		return v.Value
	}
	return ""
}

type vdfParser struct {
	s string
	i int
}

var errVDF = errors.New("malformed KeyValues file")

func (p *vdfParser) object(n *vdf, depth int, braced bool) error {
	if depth > maxVDFDepth {
		return errVDF
	}
	for {
		tok, kind, err := p.token()
		if err != nil {
			return err
		}
		switch kind {
		case tokEOF:
			if braced {
				return errVDF
			}
			return nil
		case tokClose:
			if !braced {
				return errVDF
			}
			return nil
		case tokOpen:
			return errVDF
		}
		val, vkind, err := p.token()
		if err != nil {
			return err
		}
		child := &vdf{}
		switch vkind {
		case tokString:
			child.Value = val
		case tokOpen:
			if err := p.object(child, depth+1, true); err != nil {
				return err
			}
		default:
			return errVDF
		}
		n.Kids = append(n.Kids, vdfKV{Key: tok, Node: child})
	}
}

const (
	tokEOF = iota
	tokString
	tokOpen
	tokClose
)

// token skips space, // comments and [$CONDITION] tags, and reads one token.
func (p *vdfParser) token() (string, int, error) {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.i++
		case c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '/':
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
		case c == '[':
			end := strings.IndexByte(p.s[p.i:], ']')
			if end < 0 {
				return "", 0, errVDF
			}
			p.i += end + 1
		case c == '{':
			p.i++
			return "", tokOpen, nil
		case c == '}':
			p.i++
			return "", tokClose, nil
		case c == '"':
			return p.quoted()
		default:
			start := p.i
			for p.i < len(p.s) && strings.IndexByte(" \t\r\n{}\"", p.s[p.i]) < 0 {
				p.i++
			}
			return p.s[start:p.i], tokString, nil
		}
	}
	return "", tokEOF, nil
}

func (p *vdfParser) quoted() (string, int, error) {
	var b strings.Builder
	for p.i++; p.i < len(p.s); p.i++ {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), tokString, nil
		case c == '\\' && p.i+1 < len(p.s):
			p.i++
			switch p.s[p.i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(p.s[p.i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errVDF
}
