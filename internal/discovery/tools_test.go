package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func exe(t *testing.T, path string) {
	t.Helper()
	write(t, path, "#!/bin/sh\n")
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- a test executable
		t.Fatal(err)
	}
}

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// toolsFixture is a small system: /usr/bin with a merged /bin, dpkg lists
// naming files under either, an alternative, and a home with pip, cargo,
// npm and Go programs and a flatpak export.
func toolsFixture(t *testing.T) (*Tools, map[string]Tool, []string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	usr := filepath.Join(root, "usr/bin")
	exe(t, filepath.Join(usr, "ls"))
	exe(t, filepath.Join(usr, "nano"))
	exe(t, filepath.Join(usr, "plain")) // not a page in dpkg's lists
	write(t, filepath.Join(usr, "notexec"), "")
	link(t, filepath.Join(root, "etc/alternatives/editor"), filepath.Join(usr, "editor"))
	link(t, filepath.Join(usr, "nano"), filepath.Join(root, "etc/alternatives/editor"))
	link(t, "usr/bin", filepath.Join(root, "bin"))
	local := filepath.Join(home, ".local/bin")
	exe(t, filepath.Join(local, "ls")) // shadows nothing: /usr/bin comes first in PATH below
	exe(t, filepath.Join(local, "flask"))
	site := filepath.Join(home, ".local/lib/python3.13/site-packages")
	write(t, filepath.Join(site, "flask-3.1.dist-info/METADATA"), "Name: Flask\nVersion: 3.1\n\n")
	write(t, filepath.Join(site, "flask-3.1.dist-info/RECORD"), "../../../bin/flask,sha256=x,1\nflask/__init__.py,,\n")
	exe(t, filepath.Join(home, ".cargo/bin/rg"))
	write(t, filepath.Join(home, ".cargo/.crates2.json"), `{"installs":{"ripgrep 14.1.0 (registry+https://x)":{"bins":["rg"]}}}`)
	nvm := filepath.Join(home, ".nvm/versions/node/v22")
	exe(t, filepath.Join(nvm, "lib/node_modules/@scope/tool/cli.js"))
	write(t, filepath.Join(nvm, "lib/node_modules/@scope/tool/package.json"), `{"version":"2.0.0"}`)
	link(t, "../lib/node_modules/@scope/tool/cli.js", filepath.Join(nvm, "bin/sctool"))
	exp := filepath.Join(home, ".local/share/flatpak")
	deploy(t, exp, "app", "org.example.App", "x86_64", "stable", "[Application]\n", "")
	exe(t, filepath.Join(exp, "app/org.example.App/x86_64/stable/active/export/bin/org.example.App"))
	link(t, "../../app/org.example.App/x86_64/stable/active/export/bin/org.example.App", filepath.Join(exp, "exports/bin/org.example.App"))

	dpkgDir := filepath.Join(root, "var/lib/dpkg")
	write(t, filepath.Join(dpkgDir, "status"), "Package: coreutils\nStatus: install ok installed\nArchitecture: amd64\nVersion: 9.7\n\n"+
		"Package: nano\nStatus: install ok installed\nArchitecture: amd64\nVersion: 8.4\n")
	// coreutils lists /bin/ls (the merged path), nano /usr/bin/nano.
	write(t, filepath.Join(dpkgDir, "info/coreutils.list"), "/.\n"+filepath.Join(root, "bin")+"\n"+filepath.Join(root, "bin/ls")+"\n")
	write(t, filepath.Join(dpkgDir, "info/nano.list"), filepath.Join(usr, "nano")+"\n")

	var ran []string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		ran = append(ran, name)
		switch name {
		case "apropos-all":
			return safeexec.Result{Stdout: []byte("ls (1)               - list directory contents\nnano (1) - Nano's editor\nnano (8) - not this one\n")}, nil
		case "man-page":
			if args[len(args)-1] == "ls" {
				return safeexec.Result{Stdout: []byte("LS(1)\n\nNAME\n  ls - list\n")}, nil
			}
			return safeexec.Result{ExitCode: 16}, errors.New("exit 16")
		}
		return safeexec.Result{}, errors.New("unexpected")
	}
	c := ExtraConfig{Home: home, Getenv: func(string) string { return "" }, Roots: []string{filepath.Join(root, "var/lib/flatpak")}}
	tl := NewTools(ToolsConfig{Extra: c, Dpkg: NewDpkg(DpkgConfig{Status: filepath.Join(dpkgDir, "status")}),
		Path: []string{filepath.Join(root, "bin"), usr, local}, System: []string{}, Run: run})
	tools, err := tl.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Tool{}
	for _, x := range tools {
		by[x.Name] = x
	}
	return tl, by, ran
}

func TestToolsOwners(t *testing.T) {
	tl, by, ran := toolsFixture(t)
	if _, ok := by["notexec"]; ok {
		t.Error("non-executable listed")
	}
	// /bin and /usr/bin are one directory; dpkg's /bin/ls owns /usr/bin's ls.
	ls := by["ls"]
	if ls.Manager != "apt" || ls.Package != "coreutils" || ls.Version != "9.7" || ls.Summary != "list directory contents" || len(ls.Shadowed) != 1 {
		t.Errorf("ls %+v", ls)
	}
	seen := map[string]bool{}
	for _, d := range tl.Dirs() {
		c, _ := filepath.EvalSymlinks(d)
		if seen[c] {
			t.Errorf("%s searched twice: %v", c, tl.Dirs())
		}
		seen[c] = true
	}
	if e := by["editor"]; !e.Alternative || e.Package != "nano" || e.Version != "8.4" || e.Summary != "Nano's editor" {
		t.Errorf("editor %+v", e)
	}
	if n := by["nano"]; n.Summary != "Nano's editor" { // section 1 wins over 8
		t.Errorf("nano %+v", n)
	}
	if p := by["plain"]; p.Manager != "" || p.Package != "" {
		t.Errorf("plain %+v", p)
	}
	for name, want := range map[string][3]string{
		"flask":           {"pip", "Flask", "3.1"},
		"rg":              {"cargo", "ripgrep", "14.1.0"},
		"sctool":          {"npm", "@scope/tool", "2.0.0"},
		"org.example.App": {"flatpak", "org.example.App", "stable"},
	} {
		x := by[name]
		if got := [3]string{x.Manager, x.Package, x.Version}; got != want {
			t.Errorf("%s: %v, want %v (%+v)", name, got, want, x)
		}
	}
	if !slices.Equal(ran, []string{"apropos-all"}) {
		t.Errorf("ran %v", ran)
	}
	// Cached: nothing runs again until something changes.
	_, _ = tl.List(context.Background())
	if _, ok, _ := tl.Find(context.Background(), "rg"); !ok {
		t.Error("find")
	}
}

func TestToolsMan(t *testing.T) {
	tl, _, _ := toolsFixture(t)
	for q, code := range map[string]int{"ls": 200, "nano": 404, "notatool": 404, "-H": 400, "../x": 400, "": 400} {
		rec := httptest.NewRecorder()
		tl.HandleMan(rec, httptest.NewRequest("GET", "/api/v1/tools/man?name="+q, nil))
		if rec.Code != code {
			t.Errorf("%q: %d %s", q, rec.Code, rec.Body)
		}
		if code == 200 {
			var body struct{ Text string }
			if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Text == "" {
				t.Errorf("body %s", rec.Body)
			}
		}
	}
}

func TestParseWhatis(t *testing.T) {
	m := ParseWhatis("a, b (1) - first\nb (8) - admin\nc (6x) - game\nbroken line\nd (3) - lib\n")
	if m["a"] != "first" || m["b"] != "first" || m["c"] != "game" || m["d"] != "lib" || len(m) != 4 {
		t.Fatalf("%v", m)
	}
}
