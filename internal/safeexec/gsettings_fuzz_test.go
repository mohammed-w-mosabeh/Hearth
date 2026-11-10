package safeexec

import (
	"strings"
	"testing"
)

// Gate 10: gsettings-set writes only the listed keys, each with a value of
// its own shape: a value cannot reach another key or carry a second one.
func FuzzGSettingsArgs(f *testing.F) {
	f.Add("set", "org.gnome.desktop.background", "picture-uri", "'file:///home/u/a.png'")
	f.Add("set", "org.gnome.desktop.notifications", "show-banners", "false")
	f.Add("set", "org.gnome.desktop.background", "picture-uri", "'file:///a' 'x'")
	f.Add("set", "org.gnome.desktop.lockdown", "disable-lock-screen", "true")
	validate := allowlist["gsettings-set"].Validate
	f.Fuzz(func(t *testing.T, verb, schema, key, value string) {
		if validate([]string{verb, schema, key, value}) != nil {
			return
		}
		if verb != "set" {
			t.Fatalf("accepted verb %q", verb)
		}
		if _, ok := GSettingsKeys[schema+" "+key]; !ok {
			t.Fatalf("accepted %s %s", schema, key)
		}
		switch key {
		case "show-banners":
			if value != "true" && value != "false" {
				t.Fatalf("accepted %q", value)
			}
		default:
			inner := strings.TrimSuffix(strings.TrimPrefix(value, "'file:///"), "'")
			if strings.ContainsAny(inner, "'\\ \"\n\t") || !strings.HasPrefix(value, "'file:///") {
				t.Fatalf("accepted %q", value)
			}
		}
	})
}
