package power

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestParseInhibitors(t *testing.T) {
	rows := [][]any{
		{"sleep", "GNOME Shell", "lock the screen", "delay", uint32(1000), uint32(9)},
		{"handle-power-key:handle-suspend-key", "gsd", "keys", "block", uint32(1000), uint32(10)},
		{"shutdown:idle", "<b>x</b>\n", "why\x00", "block", uint32(0), uint32(11)},
		{"sleep", "short row"},
		{"sleep", 1, 2, 3, 4, 5},
	}
	got := parseInhibitors(rows)
	if len(got) != 2 || got[0].Who != "GNOME Shell" || got[1].Who != "<b>x</b>" || got[1].Why != "why" {
		t.Fatalf("%+v", got)
	}
}

func TestParseSleeps(t *testing.T) {
	in, out := safeexec.SleepEnteredID, safeexec.SleepLeftID
	line := func(id, mode, us string) string {
		return `{"MESSAGE_ID":"` + id + `","SLEEP":"` + mode + `","__REALTIME_TIMESTAMP":"` + us + `"}` + "\n"
	}
	// Newest first: asleep now, then one full stretch, then a hostile mode.
	got := parseSleeps([]byte(line(in, "suspend", "9000000") + line(out, "suspend", "8000000") + line(in, "suspend", "7000000") +
		line(in, "<b>", "5000000") + "not json\n" + line("other", "suspend", "4000000") + line(in, "suspend", "x")))
	if len(got) != 3 || got[0] != (Sleep{9000, 0, "suspend"}) || got[1] != (Sleep{7000, 8000, "suspend"}) || got[2].Mode != "sleep" {
		t.Fatalf("%+v", got)
	}
}

func TestValidLimit(t *testing.T) {
	if n, err := validLimit(actions.Request{Target: "BAT0", Params: map[string]string{"end": "80"}}); err != nil || n != 80 {
		t.Fatal(n, err)
	}
	for _, r := range []actions.Request{
		{Target: "BAT0", Params: map[string]string{"end": "49"}}, {Target: "BAT0", Params: map[string]string{"end": "101"}},
		{Target: "BAT0", Params: map[string]string{"end": "080"}}, {Target: "BAT0", Params: map[string]string{"end": "+80"}},
		{Target: "BAT0", Params: map[string]string{"end": "x"}}, {Target: "BAT0", Params: map[string]string{}},
		{Target: "BAT0", Params: map[string]string{"end": "80", "x": "1"}}, {Target: "../x", Params: map[string]string{"end": "80"}},
	} {
		if _, err := validLimit(r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText(strings.Repeat("é", 300)); len(got) > maxSleepText || !utf8.ValidString(got) {
		t.Fatal(len(got))
	}
}

func FuzzParseSleeps(f *testing.F) {
	f.Add([]byte(`{"MESSAGE_ID":"` + safeexec.SleepEnteredID + `","SLEEP":"suspend","__REALTIME_TIMESTAMP":"1"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, s := range parseSleeps(b) {
			if !sleepMode.MatchString(s.Mode) {
				t.Fatalf("%+v", s)
			}
		}
	})
}

func FuzzValidLimit(f *testing.F) {
	f.Add("BAT0", "80")
	f.Fuzz(func(t *testing.T, target, end string) {
		n, err := validLimit(actions.Request{Target: target, Params: map[string]string{"end": end}})
		if err == nil && (n < 50 || n > 100 || strings.ContainsAny(target, "/\n\x00")) {
			t.Fatalf("%q %q", target, end)
		}
	})
}
