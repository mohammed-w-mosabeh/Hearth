package secrets

import (
	"maps"
	"strings"
	"testing"
)

// hard are values checked against real Compose v5.6 (ROADMAP 9.5): each
// came back byte for byte through --env-file and a ${KEY} reference.
var hard = []string{"end\\", "it's", "a$b${X}$$c", `"q"`, `lit\nnot newline`, "tab\there", "ü€ ✓", "  spaced  ",
	"#hash", "x # y", `back\\two`, "$(id)", "`id`", "${HOME:-z}", `\$`, `\"`, "=eq=", ""}

func TestRoundTrip(t *testing.T) {
	in := map[string]string{}
	for i, v := range hard {
		in["S"+string(rune('A'+i))] = v
	}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "\n") != len(hard) {
		t.Fatalf("%s", b)
	}
	out, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(in, out) {
		t.Fatalf("got %q", out)
	}
	if b2, _ := Encode(out); string(b2) != string(b) {
		t.Fatal("not canonical")
	}
	if m, err := Parse(nil); err != nil || len(m) != 0 {
		t.Fatal("empty file")
	}
}

func TestRefused(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"newline":      {"A": "a\nB=b"},
		"cr":           {"A": "a\rb"},
		"nul":          {"A": "a\x00b"},
		"esc":          {"A": "\x1b[2J"},
		"bad utf8":     {"A": "\xff"},
		"long":         {"A": strings.Repeat("a", MaxValue+1)},
		"key dash":     {"A-B": "x"},
		"key compose":  {"COMPOSE_FILE": "x"},
		"key docker":   {"docker_host": "x"},
		"key space":    {"A B": "x"},
		"key empty":    {"": "x"},
		"key leading9": {"9A": "x"},
	} {
		b, err := Encode(m)
		if err == nil {
			t.Errorf("%s: encoded %q", name, b)
			continue
		}
		for _, v := range m {
			if v != "" && len(v) < 100 && strings.Contains(err.Error(), v) {
				t.Errorf("%s: the error shows the value: %v", name, err)
			}
		}
	}
	big := map[string]string{}
	for i := range 5 {
		big["K"+string(rune('A'+i))] = strings.Repeat("a", MaxValue)
	}
	if _, err := Encode(big); err == nil {
		t.Error("over MaxTotal")
	}
}

func TestParseStrict(t *testing.T) {
	for name, s := range map[string]string{
		"no newline":    `A="x"`,
		"unquoted":      "A=x\n",
		"single quoted": "A='x'\n",
		"dollar":        "A=\"$x\"\n",
		"lone quote":    "A=\"a\"b\"\n",
		"bad escape":    "A=\"\\n\"\n",
		"trailing \\":   "A=\"x\\\"\n",
		"twice":         "A=\"x\"\nA=\"y\"\n",
		"comment":       "# c\nA=\"x\"\n",
		"blank":         "\nA=\"x\"\n",
		"export":        "export A=\"x\"\n",
		"compose key":   "COMPOSE_PROJECT_NAME=\"x\"\n",
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, v := range hard {
		f.Add("K", v)
	}
	f.Fuzz(func(t *testing.T, k, v string) {
		b, err := Encode(map[string]string{k: v})
		if err != nil {
			return
		}
		out, err := Parse(b)
		if err != nil {
			t.Fatalf("Parse(Encode): %v", err)
		}
		if out[k] != v || len(out) != 1 {
			t.Fatalf("%q -> %q", v, out[k])
		}
		// One line per key: a value never starts another line.
		if strings.Count(string(b), "\n") != 1 {
			t.Fatalf("%q", b)
		}
		_, _ = Parse([]byte(v))
	})
}
