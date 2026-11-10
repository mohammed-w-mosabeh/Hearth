package rules

import (
	"encoding/json"
	"path"
	"strings"
	"testing"
)

const op = "0123456789abcdef0123456789abcdef"

var sha = strings.Repeat("a", 64)

func good() Manifest {
	return Manifest{Op: op, UID: 1000, Item: "vlc", Created: 1, Entries: []Entry{
		{Path: "/etc/vlc", Kind: EntryDir, Member: MovedMember(op, 0), Archive: ArchiveName(0), SHA256: sha},
		{Path: "/usr/local/bin/tool", Kind: EntryFile, Member: MovedMember(op, 1), Archive: ArchiveName(1), SHA256: sha},
		{Path: "/etc/default/vlc", Kind: EntryConffile, Package: "vlc:amd64", Member: "etc/default/vlc", Archive: ArchiveName(2), SHA256: sha},
	}}
}

func enc(t *testing.T, m Manifest) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseManifest(t *testing.T) {
	if _, err := ParseManifest(op, enc(t, good())); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Manifest){
		"op":           func(m *Manifest) { m.Op = strings.Repeat("b", 32) },
		"no entries":   func(m *Manifest) { m.Entries = nil },
		"created":      func(m *Manifest) { m.Created = 0 },
		"traversal":    func(m *Manifest) { m.Entries[0].Path = "/etc/../root" },
		"deep":         func(m *Manifest) { m.Entries[0].Path = "/etc/vlc/x" },
		"usr":          func(m *Manifest) { m.Entries[0].Path = "/usr/bin/x" },
		"shared":       func(m *Manifest) { m.Entries[0].Path = "/etc/systemd" },
		"member":       func(m *Manifest) { m.Entries[0].Member = "etc/vlc" },
		"member index": func(m *Manifest) { m.Entries[1].Member = MovedMember(op, 0) },
		"archive":      func(m *Manifest) { m.Entries[0].Archive = "../x.tar.zst" },
		"sha":          func(m *Manifest) { m.Entries[0].SHA256 = "x" },
		"kind":         func(m *Manifest) { m.Entries[0].Kind = "device" },
		"twice":        func(m *Manifest) { m.Entries[1].Path = "/etc/vlc" },
		"conf top":     func(m *Manifest) { m.Entries[2].Path, m.Entries[2].Member = "/etc/passwd", "etc/passwd" },
		"conf hearth":  func(m *Manifest) { m.Entries[2].Path, m.Entries[2].Member = "/etc/hearth/x", "etc/hearth/x" },
		"conf polkit": func(m *Manifest) {
			m.Entries[2].Path, m.Entries[2].Member = "/etc/polkit-1/rules.d/x", "etc/polkit-1/rules.d/x"
		},
		"conf outside": func(m *Manifest) { m.Entries[2].Path, m.Entries[2].Member = "/root/.ssh/x", "root/.ssh/x" },
		"conf member":  func(m *Manifest) { m.Entries[2].Member = "etc/other" },
		"conf package": func(m *Manifest) { m.Entries[2].Package = "-x" },
		"moved pkg":    func(m *Manifest) { m.Entries[0].Package = "vlc" },
		"uid":          func(m *Manifest) { m.UID = 1<<31 + 1 },
	}
	for name, f := range bad {
		m := good()
		f(&m)
		if _, err := ParseManifest(op, enc(t, m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, raw := range []string{`{}`, `[]`, `{"op":"` + op + `","x":1}`, string(enc(t, good())) + `{}`} {
		if _, err := ParseManifest(op, []byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := ParseManifest("../x", enc(t, good())); err == nil {
		t.Error("accepted a bad op")
	}
}

func TestCheckMember(t *testing.T) {
	m := good()
	dir, file, conf := m.Entries[0], m.Entries[1], m.Entries[2]
	ok := []struct {
		e Entry
		n string
	}{{dir, dir.Member}, {dir, dir.Member + "/"}, {dir, dir.Member + "/a/b"}, {file, file.Member}, {conf, conf.Member}}
	for _, c := range ok {
		if err := CheckMember(c.e, c.n); err != nil {
			t.Errorf("%q: %v", c.n, err)
		}
	}
	for _, c := range []struct {
		e Entry
		n string
	}{{dir, "/etc/passwd"}, {dir, "../x"}, {dir, dir.Member + "/../../x"}, {dir, dir.Member + "x"}, {dir, "etc/vlc"},
		{dir, ""}, {dir, dir.Member + "/a\nb"}, {file, file.Member + "/x"}, {conf, "etc/default/other"}, {dir, dir.Member + "//a"}} {
		if err := CheckMember(c.e, c.n); err == nil {
			t.Errorf("accepted %q", c.n)
		}
	}
}

func TestValidOp(t *testing.T) {
	if !ValidOp(op) || ValidOp(strings.ToUpper(op)) || ValidOp(op[:31]) || ValidOp(op+"0") || ValidOp("../"+op[3:]) {
		t.Fatal("op shape")
	}
}

// A manifest that parses names only paths a restore may write: moved
// leftovers one level below a root, configuration files inside /etc.
func FuzzParseManifest(f *testing.F) {
	b, _ := json.Marshal(good())
	f.Add(string(b))
	f.Add(`{"op":"` + op + `","uid":0,"item":"x","created":1,"entries":[{"path":"/etc/../x","kind":"dir"}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		m, err := ParseManifest(op, []byte(raw))
		if err != nil {
			return
		}
		for i, e := range m.Entries {
			if path.Clean(e.Path) != e.Path || strings.Contains(e.Path, "..") && !strings.Contains(path.Base(e.Path), "..") {
				t.Fatalf("accepted %q", e.Path)
			}
			switch e.Kind {
			case EntryConffile:
				if !strings.HasPrefix(e.Path, "/etc/") || e.Member != e.Path[1:] {
					t.Fatalf("accepted %+v", e)
				}
			default:
				if _, _, err := CheckSystem(e.Path); err != nil || e.Member != MovedMember(op, i) {
					t.Fatalf("accepted %+v", e)
				}
			}
			if e.Archive != ArchiveName(i) {
				t.Fatalf("archive %q", e.Archive)
			}
		}
	})
}

// Whatever tar prints, an accepted member stays inside its entry.
func FuzzCheckMember(f *testing.F) {
	f.Add(MovedMember(op, 0) + "/a/b")
	f.Add("../x")
	e := good().Entries[0]
	f.Fuzz(func(t *testing.T, name string) {
		if CheckMember(e, name) != nil {
			return
		}
		n := strings.TrimSuffix(name, "/")
		if path.IsAbs(n) || path.Clean(n) != n || (n != e.Member && !strings.HasPrefix(n, e.Member+"/")) {
			t.Fatalf("accepted %q", name)
		}
		for _, el := range strings.Split(n, "/") {
			if el == ".." {
				t.Fatalf("accepted %q", name)
			}
		}
	})
}
