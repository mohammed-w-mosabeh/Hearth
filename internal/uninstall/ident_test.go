package uninstall

import (
	"strings"
	"testing"
)

func TestMatchUser(t *testing.T) {
	ids := Identifiers{
		Packages: []string{"code", "vlc"}, Desktop: []string{"org.gnome.Maps", "code"}, Exec: []string{"code", "x"},
		WMClass: []string{"Code"}, AppIDs: []string{"org.videolan.VLC"}, Snaps: []string{"firefox"},
	}.Clean()
	cases := []struct {
		root, name string
		match, pre bool
		by         string
	}{
		{".var/app", "org.videolan.VLC", true, true, "app_id"},
		{".var/app", "org.videolan.vlc", false, false, ""}, // Flatpak IDs are exact
		{".var/app", "vlc", false, false, ""},
		{"snap", "firefox", true, true, "snap"},
		{"snap", "Firefox", false, false, ""},
		{".config", "org.videolan.VLC", true, true, "app_id"},
		{".local/share", "org.gnome.Maps", true, true, "desktop"},
		{".config", "vlc", true, false, "package"},
		{".config", "VLC", true, false, "package"},
		{".config", "Code", true, false, "package"},
		{"", ".vlc", true, false, "package"},
		{"", "vlc", false, false, ""},      // not a dot-folder
		{".config", "x", false, false, ""}, // too short to guess by
		{".config", "other", false, false, ""},
	}
	for _, c := range cases {
		m, ok := ids.MatchUser(c.root, c.name)
		if ok != c.match || m.Preselect != c.pre || m.By != c.by {
			t.Errorf("%s/%s: %+v %v", c.root, c.name, m, ok)
		}
		if ok && m.Reason == "" {
			t.Errorf("%s/%s: no reason", c.root, c.name)
		}
	}
}

func TestMatchSystem(t *testing.T) {
	ids := Identifiers{Packages: []string{"mysql-server", "nginx"}, Exec: []string{"tool"}, Names: []string{"Obsidian"}}
	for name, want := range map[string]bool{"nginx": true, "Nginx": false, "mysql-server": true, "mysql": false,
		"Obsidian": true, "tool": true, "data": false} {
		m, ok := ids.MatchSystem("/etc", name)
		if ok != want || m.Preselect {
			t.Errorf("%s: %+v %v", name, m, ok)
		}
	}
	if _, ok := ids.MatchSystem("/usr/local/bin", "nginx"); ok {
		t.Error("a package name matched a file in /usr/local/bin")
	}
	if _, ok := ids.MatchSystem("/usr/local/bin", "tool"); !ok {
		t.Error("a program did not match its own name in /usr/local/bin")
	}
}

func TestStoplist(t *testing.T) {
	ids := Identifiers{Packages: []string{"data", "share"}, Exec: []string{"sh", "env"}, WMClass: []string{"Main"}}
	for _, n := range []string{"data", "share", "sh", "env", "main", "Main", ".data"} {
		if _, ok := ids.MatchUser(".config", n); ok {
			t.Errorf("guessed %s", n)
		}
	}
}

func TestClean(t *testing.T) {
	ids := Identifiers{Packages: []string{"a", "a", "", "  ", "../x", "-rf", "b/c", strings.Repeat("y", 300), "ok"}}.Clean()
	if strings.Join(ids.Packages, ",") != "a,ok" {
		t.Fatalf("%q", ids.Packages)
	}
}

// A match is preselected only for an exact ID; a guess never is, and
// nothing matches a name too short or too generic to guess by.
func FuzzMatchUser(f *testing.F) {
	f.Add(".config", "vlc", "vlc", "org.x.Y")
	f.Add(".var/app", "org.x.Y", "x", "org.x.Y")
	f.Add("", ".data", "data", "a.b.c")
	f.Fuzz(func(t *testing.T, root, name, pkg, appID string) {
		ids := Identifiers{Packages: []string{pkg}, Exec: []string{pkg}, AppIDs: []string{appID}, Snaps: []string{pkg}}.Clean()
		m, ok := ids.MatchUser(root, name)
		if !ok {
			return
		}
		if m.Reason == "" || m.Value == "" {
			t.Fatalf("%+v", m)
		}
		if m.Preselect {
			bare := strings.TrimPrefix(name, ".")
			if bare != m.Value && name != m.Value {
				t.Fatalf("preselected %q for %q", name, m.Value)
			}
			return
		}
		f := fold(name)
		if !guessable(f) || fold(m.Value) != f {
			t.Fatalf("guessed %q by %q", name, m.Value)
		}
		if root == ".var/app" || root == "snap" {
			t.Fatalf("guessed in %s", root)
		}
	})
}
