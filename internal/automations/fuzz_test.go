package automations

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
)

// FuzzNotifyText: whatever the text, the event and the journal hold, a
// notification's body is bounded, holds no control character but a
// newline, and never the line that names a secret (S19).
func FuzzNotifyText(f *testing.F) {
	f.Add("{automation}: {trigger} on {instance} {value}{journal}", "user/x.service", "started")
	f.Add("{top} {sources} {outage} {alert} {nope} {", "a\x1b[31m", "https://u:p@example.com/x")
	f.Add("{journal}", "system/\n", strings.Repeat("A", 70))
	f.Fuzz(func(t *testing.T, text, instance, line string) {
		const canary = "CANARY9f3a"
		s := Steps{
			Top: func(context.Context) ([]string, error) { return []string{line, "b", "c", "d"}, nil },
			Journal: func(context.Context, string, int) ([]string, error) {
				return []string{line + " password=" + canary, "ok"}, nil
			},
		}
		ev := Event{Kind: "ssh_failed", Instance: instance, Value: 3, Outage: 61, Detail: line, Alert: line}
		out := Mask(s.fill(context.Background(), text, ev, line))
		if len(out) > maxNotice+len("…") || !utf8.ValidString(strings.ToValidUTF8(out, "")) {
			t.Fatalf("%d bytes", len(out))
		}
		for _, r := range out {
			if r != '\n' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0)) {
				t.Fatalf("control character %U in %q", r, out)
			}
		}
		// The canary can only arrive through the journal's line, which
		// names a secret; the fuzzed inputs may hold the word themselves.
		if strings.Contains(out, canary) && !strings.Contains(text+instance+line, canary) {
			t.Fatalf("a secret's line got through: %q", out)
		}
		// Text the validator accepts holds only known placeholders, and
		// none is left unfilled.
		st := spec.Step{Step: "notify", Channels: []string{"desktop"}, Text: text}
		if (spec.Spec{Trigger: spec.Trigger{Kind: "login"}, Then: []spec.Step{st}}).Check() == nil {
			filled := s.fill(context.Background(), text, Event{Kind: "login"}, "name")
			if strings.ContainsAny(filled, "{}") {
				t.Fatalf("accepted text %q leaves a brace: %q", text, filled)
			}
		}
	})
}
