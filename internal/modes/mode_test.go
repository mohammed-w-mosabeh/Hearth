package modes

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

const work = `
name = "Work"

[session]
wallpaper = "~/Pictures/desk.jpg"
do_not_disturb = true
brightness = 70
power_profile = "balanced"
open = ["org.gnome.TextEditor"]
connect = ["0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"]

[[stack]]
name = "notes"
do = "start"

[[stack]]
name = "photos"
do = "stop"

[[item]]
kind = "unit"
target = "user/syncnotes.service"
do = "start"

[[trigger]]
kind = "schedule"
on_calendar = "Mon..Fri 09:00"

[[trigger]]
kind = "wifi"
connection = "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"

[[trigger]]
kind = "battery"
below = 30

[[trigger]]
kind = "display"
event = "connect"

[[trigger]]
kind = "game"
`

func TestParseValid(t *testing.T) {
	m, err := Parse("work", []byte(work))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "work" || m.Name != "Work" || len(m.Hash) != 64 || m.Hash != Hash([]byte(work)) {
		t.Fatalf("%+v", m)
	}
	s := m.Session
	if s.Wallpaper != "~/Pictures/desk.jpg" || s.DoNotDisturb == nil || !*s.DoNotDisturb || *s.Brightness != 70 || s.PowerProfile != "balanced" {
		t.Fatalf("%+v", s)
	}
	if len(m.Stacks) != 2 || m.Stacks[1] != (StackAction{"photos", "stop"}) || len(m.Items) != 1 || len(m.Triggers) != 5 || *m.Triggers[2].Below != 30 {
		t.Fatalf("%+v", m)
	}
	items := m.ActionItems()
	if len(items) != 2 || items[0].Kind != "unit" || items[1].Kind != "app" || items[1].Target != "org.gnome.TextEditor" || items[1].Do != "start" {
		t.Fatalf("%+v", items)
	}
	// The smallest mode: one session setting, no lists.
	m, err = Parse("quiet", []byte("name = \"Quiet\"\n[session]\ndo_not_disturb = false\n"))
	if err != nil || m.Stacks == nil || m.Items == nil || m.Triggers == nil || *m.Session.DoNotDisturb {
		t.Fatalf("%v %+v", err, m)
	}
	if _, err := Parse("sys", []byte("name = \"Sys\"\n[session]\nwallpaper = \"/usr/share/backgrounds/gnome/blobs-l.svg\"\n")); err != nil {
		t.Fatal(err)
	}
}

func TestParseRefused(t *testing.T) {
	const base = "name = \"M\"\n"
	cases := map[string]struct{ id, src, want string }{
		"bad id":           {"../x", base + "[session]\nbrightness = 5\n", "a mode file is named"},
		"upper id":         {"Work", base + "[session]\nbrightness = 5\n", "a mode file is named"},
		"dash id":          {"-x", base + "[session]\nbrightness = 5\n", "a mode file is named"},
		"command":          {"m", base + "command = \"rm -rf ~\"\n[session]\nbrightness = 5\n", `unknown key "command"`},
		"session command":  {"m", base + "[session]\nexec = \"sh\"\n", `unknown key "session.exec"`},
		"stack spec":       {"m", base + "[[stack]]\nname = \"a\"\ndo = \"start\"\nimage = \"evil\"\n", `unknown key "stack.image"`},
		"trigger command":  {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"game\"\nrun = \"x\"\n", `unknown key "trigger.run"`},
		"no name":          {"m", "[session]\nbrightness = 5\n", "name is 1 to"},
		"long name":        {"m", "name = \"" + strings.Repeat("a", 41) + "\"\n[session]\nbrightness = 5\n", "name is 1 to"},
		"name newline":     {"m", "name = \"a\\nb\"\n[session]\nbrightness = 5\n", "one line"},
		"name bidi":        {"m", "name = \"a\\u202eb\"\n[session]\nbrightness = 5\n", "one line"},
		"name space":       {"m", "name = \" a\"\n[session]\nbrightness = 5\n", "space"},
		"empty":            {"m", base, "at least one thing"},
		"syntax":           {"m", base + "[session\n", "line 3"},
		"duplicate key":    {"m", base + "name = \"N\"\n", "line 2"},
		"wrong type":       {"m", base + "[session]\nbrightness = \"high\"\n", "brightness"},
		"brightness 0":     {"m", base + "[session]\nbrightness = 0\n", "brightness is 1 to 100"},
		"brightness big":   {"m", base + "[session]\nbrightness = 9223372036854775807\n", "brightness is 1 to 100"},
		"power":            {"m", base + "[session]\npower_profile = \"turbo\"\n", "power_profile"},
		"wallpaper etc":    {"m", base + "[session]\nwallpaper = \"/etc/shadow.png\"\n", "your home"},
		"wallpaper dotdot": {"m", base + "[session]\nwallpaper = \"~/../root/a.png\"\n", "not clean"},
		"wallpaper sys..":  {"m", base + "[session]\nwallpaper = \"/usr/share/backgrounds/../../../etc/a.png\"\n", "not clean"},
		"wallpaper double": {"m", base + "[session]\nwallpaper = \"~//etc/a.png\"\n", "not clean"},
		"wallpaper ext":    {"m", base + "[session]\nwallpaper = \"~/.bashrc\"\n", "JPEG"},
		"wallpaper ctrl":   {"m", base + "[session]\nwallpaper = \"~/a\\u0007.png\"\n", "control"},
		"open space":       {"m", base + "[session]\nopen = [\"a b\"]\n", "one word"},
		"open twice":       {"m", base + "[session]\nopen = [\"a\", \"a\"]\n", "twice"},
		"open many":        {"m", base + "[session]\nopen = [" + strings.Repeat(`"a",`, 16) + "\"b\"]\n", "at most 16"},
		"ssid":             {"m", base + "[session]\nconnect = [\"HomeWiFi\"]\n", "not a connection UUID"},
		"conn both":        {"m", base + "[session]\nconnect = [\"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11\"]\ndisconnect = [\"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11\"]\n", "both"},
		"stack name":       {"m", base + "[[stack]]\nname = \"../x\"\ndo = \"stop\"\n", "not a stack name"},
		"stack lookalike":  {"m", base + "[[stack]]\nname = \"рhotos\"\ndo = \"stop\"\n", "not a stack name"},
		"stack verb":       {"m", base + "[[stack]]\nname = \"a\"\ndo = \"remove\"\n", "start or stop"},
		"stack twice":      {"m", base + "[[stack]]\nname = \"a\"\ndo = \"stop\"\n[[stack]]\nname = \"a\"\ndo = \"start\"\n", "twice"},
		"stack many":       {"m", base + strings.Repeat("[[stack]]\nname = \"a\"\ndo = \"stop\"\n", 33), "at most 32"},
		"item app":         {"m", base + "[[item]]\nkind = \"app\"\ntarget = \"x\"\ndo = \"start\"\n", "session.open"},
		"item kind":        {"m", base + "[[item]]\nkind = \"shell\"\ntarget = \"x\"\ndo = \"start\"\n", "kind is unit"},
		"item target":      {"m", base + "[[item]]\nkind = \"unit\"\ntarget = \"\"\ndo = \"start\"\n", "target is 1 to"},
		"item verb":        {"m", base + "[[item]]\nkind = \"unit\"\ntarget = \"user/a.service\"\ndo = \"mask\"\n", "start or stop"},
		"trigger kind":     {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"boot\"\n", "kind is schedule"},
		"trigger ssid":     {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"wifi\"\nconnection = \"Cafe\"\n", "SSID"},
		"trigger mixed":    {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"game\"\nbelow = 20\n", "has no below"},
		"trigger battery":  {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"battery\"\nbelow = 2\n", "5 to 95"},
		"trigger calendar": {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"schedule\"\non_calendar = \"; reboot\"\n", "on_calendar"},
		"trigger display":  {"m", base + "[session]\nbrightness = 5\n[[trigger]]\nkind = \"display\"\nevent = \"hotplug\"\n", "connect or disconnect"},
		"trigger many":     {"m", base + "[session]\nbrightness = 5\n" + strings.Repeat("[[trigger]]\nkind = \"game\"\n", 9), "at most 8"},
		"not utf8":         {"m", base + "# \xff\n[session]\nbrightness = 5\n", "UTF-8"},
		"big":              {"m", base + "#" + strings.Repeat("a", MaxFileSize) + "\n", "larger than"},
	}
	for name, c := range cases {
		_, err := Parse(c.id, []byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

// An unknown key's name is shown cleaned: it cannot carry control characters.
func TestParseCleansKeys(t *testing.T) {
	_, err := Parse("m", []byte("name = \"M\"\n\"a\\u001b[31mb\" = 1\n"))
	if err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("%q", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("work", work)
	f.Add("m", "name = \"M\"\n[session]\nbrightness = 5\n")
	f.Add("m", "name = \"M\"\n[[trigger]]\nkind = \"wifi\"\nconnection = \"x\"\n")
	f.Add("m", "a = { b = [1, 2, { c = 'd' }] }\n[x.y.z]\n")
	f.Fuzz(func(t *testing.T, id, src string) {
		m, err := Parse(id, []byte(src))
		if err != nil {
			msg := err.Error()
			if utf8.RuneCountInString(msg) > 400 {
				t.Fatalf("long error: %d", len(msg))
			}
			for _, r := range msg {
				if r != ' ' && !unicode.IsGraphic(r) {
					t.Fatalf("error shows %U: %q", r, msg)
				}
			}
			return
		}
		// A mode that passed holds only checked names.
		if !ValidID(m.ID) || m.check() != nil {
			t.Fatalf("accepted %+v", m)
		}
		if m.Session.empty() && len(m.Stacks) == 0 && len(m.Items) == 0 {
			t.Fatal("accepted an empty mode")
		}
	})
}
