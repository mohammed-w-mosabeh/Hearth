package pathcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func specJSON(t *testing.T, roots []string, binds ...string) []byte {
	t.Helper()
	vols := []map[string]any{}
	for i, b := range binds {
		vols = append(vols, map[string]any{"type": "bind", "source": b, "target": fmt.Sprintf("/m%d", i)})
	}
	svc := map[string]any{"image": "nginx:1.27"}
	if len(vols) > 0 {
		svc["volumes"] = vols
	}
	m := map[string]any{"version": 1, "name": "probe", "services": map[string]any{"web": svc}}
	if len(roots) > 0 {
		m["bind_roots"] = roots
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake resolves through a table, all on mount 7 of /dev 8:1 at /, except
// /mnt/disk on mount 9.
func fake(table map[string]string) Resolver {
	return Resolver{
		Open: func(p string) (string, uint64, error) {
			r, ok := table[p]
			switch {
			case !ok:
				return "", 0, unix.ENOENT
			case r == "ELOOP":
				return "", 0, unix.ELOOP
			case strings.HasPrefix(r, "/mnt/disk"):
				return r, 9, nil
			}
			return r, 7, nil
		},
		Mounts: func() ([]byte, error) {
			return []byte("7 1 8:1 / / rw - ext4 /dev/sda1 rw\n9 7 8:17 /@media /mnt/disk rw - btrfs /dev/sdb1 rw\n"), nil
		},
	}
}

func TestCheck(t *testing.T) {
	res := fake(map[string]string{
		"/srv/app": "/srv/app", "/srv/app/data": "/srv/app/data",
		"/srv/app/esc": "/etc", "/srv/app/chain": "/srv/other",
		"/srv/app/magic": "ELOOP", "/home/u/m": "/mnt/disk/m", "/home/u/m/f": "/mnt/disk/m/f",
		"/home/u/bad": "/etc", "/srv/app/sock": "/run/docker.sock",
	})
	r := res.Check(specJSON(t, []string{"/srv/app", "/home/u/m"}, "/srv/app/data", "/home/u/m/f"))
	if !r.OK || len(r.Refused) != 0 {
		t.Fatalf("refused: %v", r.Refused)
	}
	want := []Resolved{
		{Real: "/srv/app/data", Dev: "8:1", FSRoot: "/srv/app/data"},
		{Real: "/mnt/disk/m/f", Dev: "8:17", FSRoot: "/@media/m/f"},
	}
	for i, w := range want {
		if g := r.Binds[i]; g.Real != w.Real || g.Dev != w.Dev || g.FSRoot != w.FSRoot || g.Service != "web" {
			t.Errorf("bind %d: %+v", i, g)
		}
	}
	if r.Roots["/home/u/m"] != "/mnt/disk/m" {
		t.Errorf("roots: %v", r.Roots)
	}
	if !Same(r, res.Check(specJSON(t, []string{"/srv/app", "/home/u/m"}, "/srv/app/data", "/home/u/m/f"))) {
		t.Error("the same check differs")
	}

	for name, c := range map[string]struct {
		roots []string
		binds []string
		why   string
	}{
		"escape":      {[]string{"/srv/app"}, []string{"/srv/app/esc"}, "never allowed"},
		"chain":       {[]string{"/srv/app"}, []string{"/srv/app/chain"}, "outside its approved root"},
		"magic link":  {[]string{"/srv/app"}, []string{"/srv/app/magic"}, "/proc link"},
		"missing":     {[]string{"/srv/app"}, []string{"/srv/app/nope"}, "does not exist"},
		"socket":      {[]string{"/srv/app"}, []string{"/srv/app/sock"}, "its own opt-in"},
		"root to etc": {[]string{"/home/u/bad"}, nil, "cannot be a root"},
		"lexical":     {[]string{"/srv/app"}, []string{"/etc/passwd"}, "never allowed"},
		"traversal":   {[]string{"/srv/app"}, []string{"/srv/app/../../etc"}, "absolute, clean"},
		"trailing":    {[]string{"/srv/app"}, []string{"/srv/app/data/"}, "absolute, clean"},
		"proc root":   {[]string{"/srv/app"}, []string{"/proc/self/root/etc"}, "never allowed"},
		"into hearth": {[]string{"/srv/app"}, []string{"/var/lib/hearth/stacks"}, "never allowed"},
	} {
		r := res.Check(specJSON(t, c.roots, c.binds...))
		if r.OK || len(r.Binds) != 0 || !strings.Contains(strings.Join(r.Refused, "|"), c.why) {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestSame(t *testing.T) {
	a := Reply{OK: true, Roots: map[string]string{"/a": "/a"}, Binds: []Resolved{{Real: "/a/x", Dev: "8:1", FSRoot: "/a/x"}}}
	b := a
	b.Binds = []Resolved{{Real: "/a/x", Dev: "8:1", FSRoot: "/a/y"}}
	if !Same(a, a) || Same(a, b) || Same(a, Reply{}) {
		t.Error("Same")
	}
	c := a
	c.Roots = map[string]string{"/a": "/b"}
	if Same(a, c) {
		t.Error("a moved root is the same")
	}
}

func TestVerifyMounts(t *testing.T) {
	binds := []Resolved{
		{Real: "/srv/app/data", Dev: "8:1", FSRoot: "/srv/app/data"},
		{Real: "/mnt/disk/m f", Dev: "8:17", FSRoot: "/@media/m f"},
	}
	binds[0].Service, binds[0].Target = "web", "/data"
	binds[1].Service, binds[1].Target = "web", "/media"
	good := "600 500 0:50 / / rw - overlay overlay rw\n" +
		"601 600 8:1 /srv/app/data /data rw - ext4 /dev/sda1 rw\n" +
		"602 600 8:17 /@media/m\\040f /media rw - btrfs /dev/sdb1 rw\n"
	if bad := VerifyMounts([]byte(good), binds); len(bad) != 0 {
		t.Fatal(bad)
	}
	swapped := strings.Replace(good, "/srv/app/data /data", "/etc /data", 1)
	if bad := VerifyMounts([]byte(swapped), binds); len(bad) != 1 || !strings.Contains(bad[0], "/data") {
		t.Fatal(bad)
	}
	// A later mount on the same target is the one the container sees.
	over := good + "603 600 8:1 /etc /data rw - ext4 /dev/sda1 rw\n"
	if bad := VerifyMounts([]byte(over), binds); len(bad) != 1 {
		t.Fatal(bad)
	}
	if bad := VerifyMounts([]byte("600 500 0:50 / / rw - overlay overlay rw\n"), binds); len(bad) != 2 {
		t.Fatal(bad)
	}
	otherDev := strings.Replace(good, "8:17", "8:33", 1)
	if bad := VerifyMounts([]byte(otherDev), binds); len(bad) != 1 {
		t.Fatal(bad)
	}
}

func TestServe(t *testing.T) {
	res := fake(map[string]string{"/srv/app": "/srv/app", "/srv/app/d": "/srv/app/d"})
	for name, c := range map[string]struct {
		in string
		ok bool
	}{
		"good":    {`{"spec":` + string(specJSON(t, []string{"/srv/app"}, "/srv/app/d")) + "}\n", true},
		"unknown": {`{"spec":{},"x":1}` + "\n", false},
		"empty":   {"\n", false},
		"two":     {`{"spec":{}} {"spec":{}}` + "\n", false},
		"large":   {`{"spec":"` + strings.Repeat("a", MaxRequest) + `"}` + "\n", false},
		"yaml":    {`{"spec":"services:\n  a: {}"}` + "\n", false},
	} {
		var out bytes.Buffer
		if err := Serve(res, strings.NewReader(c.in), &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var r Reply
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.OK != c.ok || (!c.ok && len(r.Refused) == 0) {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

// TestSystem resolves real links on this kernel.
func TestSystem(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "approved")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "data"), 0o700))
	must(os.MkdirAll(filepath.Join(dir, "outside"), 0o700))
	must(os.Symlink(filepath.Join(dir, "outside"), filepath.Join(root, "escape")))
	must(os.Symlink("escape", filepath.Join(root, "chain")))
	must(os.Symlink("data", filepath.Join(root, "inside")))
	must(os.Symlink("/proc/self/root/etc", filepath.Join(root, "magic")))
	must(os.Symlink("/etc", filepath.Join(root, "etc")))
	must(os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop")))

	r := System.Check(specJSON(t, []string{root}, filepath.Join(root, "data"), filepath.Join(root, "inside")))
	if !r.OK {
		t.Fatalf("refused: %v", r.Refused)
	}
	if r.Binds[1].Real != filepath.Join(root, "data") || r.Binds[0].Dev == "" || !strings.HasSuffix(r.Binds[0].FSRoot, "/approved/data") {
		t.Errorf("binds: %+v", r.Binds)
	}
	for link, why := range map[string]string{
		"escape": "outside its approved root",
		"chain":  "outside its approved root",
		"magic":  "/proc link",
		"etc":    "never allowed",
		"loop":   "too many links",
		"nope":   "does not exist",
	} {
		r := System.Check(specJSON(t, []string{root}, filepath.Join(root, link)))
		if r.OK || !strings.Contains(strings.Join(r.Refused, "|"), why) {
			t.Errorf("%s: %+v", link, r)
		}
	}
	// A root that is itself a link is resolved, then checked.
	must(os.Symlink("/etc", filepath.Join(dir, "etcroot")))
	if r := System.Check(specJSON(t, []string{filepath.Join(dir, "etcroot")})); r.OK {
		t.Error("a root linked to /etc was accepted")
	}
	// Nothing was written.
	n := 0
	must(filepath.WalkDir(dir, func(string, fs.DirEntry, error) error { n++; return nil }))
	if n != 11 {
		t.Errorf("%d entries under the test folder", n)
	}
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{`a\040b`: "a b", `a\134b`: `a\b`, `a\04`: `a\04`, `x\`: `x\`, `\011`: "\t"} {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q", in, got)
		}
	}
}

func FuzzServe(f *testing.F) {
	f.Add([]byte(`{"spec":{"version":1,"name":"a","bind_roots":["/srv"],"services":{"w":{"image":"x","volumes":[{"type":"bind","source":"/srv/a","target":"/a"}]}}}}`))
	f.Add([]byte("7 1 8:1 / / rw\n"))
	res := fake(map[string]string{"/srv": "/srv", "/srv/a": "/etc"})
	f.Fuzz(func(t *testing.T, b []byte) {
		var out bytes.Buffer
		if err := Serve(res, bytes.NewReader(b), &out); err != nil {
			return
		}
		var r Reply
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.OK && len(r.Refused) != 0 || !r.OK && len(r.Refused) == 0 {
			t.Fatalf("%+v", r)
		}
		for _, x := range r.Binds {
			if !strings.HasPrefix(x.Real, "/srv") {
				t.Fatalf("accepted %+v", x)
			}
		}
		_ = parseMountinfo(b)
		_ = unescape(string(b))
	})
}
