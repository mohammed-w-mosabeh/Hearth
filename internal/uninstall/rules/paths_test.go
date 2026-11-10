package rules

import (
	"path"
	"slices"
	"strings"
	"testing"
)

const home = "/home/u"

func TestCheckSystem(t *testing.T) {
	ok := map[string][2]string{
		"/etc/vlc":            {"/etc", "vlc"},
		"/var/lib/mysql":      {"/var/lib", "mysql"},
		"/var/cache/foo-bar":  {"/var/cache", "foo-bar"},
		"/var/log/nginx":      {"/var/log", "nginx"},
		"/opt/Obsidian":       {"/opt", "Obsidian"},
		"/usr/local/bin/tool": {"/usr/local/bin", "tool"},
		"/etc/libfoo2.0+x":    {"/etc", "libfoo2.0+x"},
	}
	for p, want := range ok {
		root, name, err := CheckSystem(p)
		if err != nil || root != want[0] || name != want[1] {
			t.Errorf("%s: %q %q %v", p, root, name, err)
		}
	}
	for _, p := range []string{
		"", "etc/vlc", "/etc", "/etc/", "/etc/vlc/", "/etc/vlc/conf", "/etc/../root", "/etc/./vlc", "/etc//vlc",
		"/etc/-rf", "/etc/.hidden", "/etc/a b", "/etc/a\nb", "/etc/a\x00b", "/usr/bin/vlc", "/usr/share/vlc",
		"/usr/local/share/x", "/home/u/.config/x", "/root/x", "/var/lib", "/var/lib/dpkg", "/var/lib/hearth",
		"/etc/systemd", "/etc/apt", "/etc/ssh", "/opt/local", "/var/tmp/x", "/etc/\u0085x", "/etc/" + strings.Repeat("a", 300),
	} {
		if _, _, err := CheckSystem(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

func TestCheckUser(t *testing.T) {
	ok := map[string][2]string{
		home + "/.config/vlc":                 {".config", "vlc"},
		home + "/.config/Code - OSS":          {".config", "Code - OSS"},
		home + "/.local/share/org.gnome.Maps": {".local/share", "org.gnome.Maps"},
		home + "/.local/state/foo":            {".local/state", "foo"},
		home + "/.cache/spotify":              {".cache", "spotify"},
		home + "/.var/app/org.x.Y":            {".var/app", "org.x.Y"},
		home + "/snap/firefox":                {"snap", "firefox"},
		home + "/.vscode":                     {"", ".vscode"},
		home + "/.docker":                     {"", ".docker"},
	}
	for p, want := range ok {
		root, name, err := CheckUser(home, p)
		if err != nil || root != want[0] || name != want[1] {
			t.Errorf("%s: %q %q %v", p, root, name, err)
		}
	}
	for _, p := range []string{
		home, home + "/", home + "/.config", home + "/.local", home + "/.local/share", home + "/Documents",
		home + "/.config/vlc/x", home + "/.config/autostart", home + "/.config/systemd", home + "/.config/hearth",
		home + "/.local/share/applications", home + "/.local/share/Trash", home + "/.local/share/hearth",
		home + "/.ssh", home + "/.gnupg", home + "/.bashrc", home + "/.config/-x", home + "/.config/ x",
		home + "/.config/x ", home + "/.config/../.ssh", home + "/.config/.", "/home/other/.config/vlc",
		"/home/u2/.config/vlc", home + "/.config/a\tb", home + "/.cache/thumbnails", home + "/..", home + "/.",
		home + "/.var/app", home + "/snap", home + "/.local/bin/x",
	} {
		if _, _, err := CheckUser(home, p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, h := range []string{"", "/", "home/u", "/home/u/", "/home/../root"} {
		if _, _, err := CheckUser(h, h+"/.config/x"); err == nil {
			t.Errorf("accepted home %q", h)
		}
	}
}

// Whatever path is given, an accepted one is clean, absolute, exactly one
// element below a root, and never shared or under /usr outside
// /usr/local/bin.
func FuzzCheckSystem(f *testing.F) {
	for _, s := range []string{"/etc/vlc", "/usr/local/bin/x", "/etc/../x", "/opt/-x", "/var/lib/a/b", "/etc/hearth"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		root, name, err := CheckSystem(p)
		if err != nil {
			return
		}
		if path.Clean(p) != p || !path.IsAbs(p) || path.Dir(p) != root || path.Base(p) != name {
			t.Fatalf("%q -> %q %q", p, root, name)
		}
		if !slices.Contains(SystemRoots, root) || sharedSystem[name] || strings.HasPrefix(name, "-") ||
			strings.HasPrefix(name, ".") || strings.ContainsAny(name, " /\\\x00\n") {
			t.Fatalf("accepted %q", p)
		}
		if strings.HasPrefix(p, "/usr/") && root != "/usr/local/bin" {
			t.Fatalf("accepted %q under /usr", p)
		}
	})
}

func FuzzCheckUser(f *testing.F) {
	for _, s := range []string{".config/vlc", ".vscode", ".ssh", ".config/../.ssh", ".var/app/org.x.Y", "snap/a", ".config/a b"} {
		f.Add(home + "/" + s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		root, name, err := CheckUser(home, p)
		if err != nil {
			return
		}
		rel, ok := strings.CutPrefix(p, home+"/")
		if !ok || path.Clean(p) != p || path.Join(home, root, name) != p {
			t.Fatalf("%q -> %q %q", p, root, name)
		}
		if strings.Contains(rel, "..") && !strings.Contains(name, "..") {
			t.Fatalf("accepted %q", p)
		}
		bare := strings.TrimPrefix(name, ".")
		if root == "" && (bare == name || sharedUser[""][bare]) {
			t.Fatalf("accepted %q in the home itself", p)
		}
		if root != "" && (sharedUser[root] == nil || sharedUser[root][name]) {
			t.Fatalf("accepted %q", p)
		}
		if strings.HasPrefix(bare, "-") || strings.HasPrefix(bare, " ") || strings.HasSuffix(name, " ") ||
			strings.ContainsAny(name, "/\\\x00\n\t") || name == "." || name == ".." {
			t.Fatalf("accepted name %q", name)
		}
	})
}
