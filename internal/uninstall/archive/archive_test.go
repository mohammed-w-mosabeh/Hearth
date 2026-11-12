package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"
)

const op = "0123456789abcdef0123456789abcdef"

// fakeTar keeps a member's tree as JSON: enough to check what is archived,
// listed, extracted and printed, without running tar.
type node struct {
	Name string      `json:"name"`
	Mode fs.FileMode `json:"mode"`
	Link string      `json:"link,omitempty"`
	Data string      `json:"data,omitempty"`
}

func fakeTar(t *testing.T, fail string) Tar {
	return func(_ context.Context, name string, args []string) ([]byte, bool, error) {
		if name == fail {
			return nil, false, errors.New("tar failed")
		}
		arg := func(flag string) string {
			for i, a := range args {
				if a == flag {
					return args[i+1]
				}
			}
			return ""
		}
		file, dir, member := arg("--file"), arg("-C"), args[len(args)-1]
		switch name {
		case "leftovers-tar-create":
			var nodes []node
			_ = filepath.WalkDir(filepath.Join(dir, member), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(dir, p)
				fi, _ := d.Info()
				n := node{Name: rel, Mode: fi.Mode()}
				switch {
				case fi.Mode()&fs.ModeSymlink != 0:
					n.Link, _ = os.Readlink(p)
				case fi.Mode().IsRegular():
					b, _ := os.ReadFile(p) // #nosec G304 G122 -- a test fixture
					n.Data = string(b)
				}
				nodes = append(nodes, n)
				return nil
			})
			if len(nodes) == 0 {
				return nil, false, errors.New("nothing to archive")
			}
			b, _ := json.Marshal(nodes)
			return nil, false, os.WriteFile(file, b, 0o600)
		}
		b, err := os.ReadFile(arg("--file"))
		if err != nil {
			return nil, false, err
		}
		var nodes []node
		_ = json.Unmarshal(b, &nodes)
		switch name {
		case "leftovers-tar-list":
			var out []string
			for _, n := range nodes {
				out = append(out, n.Name)
			}
			return []byte(strings.Join(out, "\n") + "\n"), false, nil
		case "leftovers-tar-cat":
			for _, n := range nodes {
				if n.Name == member {
					return []byte(n.Data), false, nil
				}
			}
			return nil, false, errors.New("no member")
		case "leftovers-tar-extract":
			for _, n := range nodes {
				p := filepath.Join(dir, n.Name)
				if _, err := os.Lstat(p); err == nil {
					return nil, false, errors.New("would overwrite " + p)
				}
				switch {
				case n.Mode.IsDir():
					_ = os.MkdirAll(p, 0o700)
				case n.Link != "":
					_ = os.Symlink(n.Link, p)
				default:
					_ = os.WriteFile(p, []byte(n.Data), 0o600)
				}
			}
			return nil, false, nil
		}
		t.Fatalf("unexpected tar %s", name)
		return nil, false, nil
	}
}

type rig struct {
	r   Roots
	sys string
}

func newRig(t *testing.T) rig {
	t.Helper()
	RootUID = uint32(os.Getuid()) // #nosec G115 -- a test uid
	t.Cleanup(func() { RootUID = 0 })
	base := t.TempDir()
	sys := filepath.Join(base, "sys")
	r := Roots{Uninstall: filepath.Join(base, "uninstall"), Removed: filepath.Join(base, "removed"),
		DpkgStatus: filepath.Join(base, "status"), DpkgInfo: filepath.Join(base, "info"), System: sys}
	for _, d := range []string{"etc", "var/lib", "opt", "usr/local/bin", "etc/vlc"} {
		if err := os.MkdirAll(filepath.Join(sys, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(r.OpDir(op), 0o700); err != nil {
		t.Fatal(err)
	}
	return rig{r: r, sys: sys}
}

func (g rig) file(t *testing.T, p, s string) {
	t.Helper()
	full := filepath.Join(g.sys, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (g rig) move(t *testing.T, p string) Move {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(g.sys, p))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return Move{Path: p, Dev: st.Dev, Ino: st.Ino, Kind: Kind(fi.Mode())}
}

func TestArchiveAndRestore(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	g.file(t, "/opt/Foo/bin/foo", "binary")
	if err := os.Symlink("bin/foo", filepath.Join(g.sys, "opt/Foo/foo")); err != nil {
		t.Fatal(err)
	}
	g.file(t, "/var/lib/foo/state.db", "data")
	g.file(t, "/usr/local/bin/foo", "script")
	g.file(t, "/etc/vlc/vlcrc", "edited by the user")
	job := Job{Op: op, UID: 1000, Item: "Foo",
		Conffiles: []Conffile{{Package: "vlc:amd64", Path: "/etc/vlc/vlcrc"}, {Package: "vlc:amd64", Path: "/etc/vlc/gone"}},
		Moves:     []Move{g.move(t, "/opt/Foo"), g.move(t, "/var/lib/foo"), g.move(t, "/usr/local/bin/foo")}}
	if err := WriteJob(g.r, job); err != nil {
		t.Fatal(err)
	}
	if err := RunArchive(ctx, g.r, op, fakeTar(t, "")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"opt/Foo", "var/lib/foo", "usr/local/bin/foo"} {
		if _, err := os.Lstat(filepath.Join(g.sys, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there", p)
		}
	}
	if _, err := os.Stat(filepath.Join(g.sys, "etc/vlc/vlcrc")); err != nil {
		t.Error("a conffile is copied, not moved")
	}
	m, ok, err := ReadManifest(g.r, op)
	if err != nil || !ok || len(m.Entries) != 4 || m.UID != 1000 {
		t.Fatalf("%+v %v %v", m, ok, err)
	}
	kinds := []string{m.Entries[0].Kind, m.Entries[1].Kind, m.Entries[2].Kind, m.Entries[3].Kind}
	if strings.Join(kinds, ",") != "conffile,dir,dir,file" {
		t.Fatalf("%v", kinds)
	}
	if fi, err := os.Stat(g.r.RemovedOp(op)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("archive folder %v %v", fi.Mode(), err)
	}
	if got := ListManifests(g.r, 1000); len(got) != 1 || len(ListManifests(g.r, 1001)) != 0 || len(ListManifests(g.r, 0)) != 1 {
		t.Fatal("list by user")
	}

	// Restore: /var/lib/foo exists again and is left alone; the rest comes
	// back. The conffile goes back only once the package is installed.
	g.file(t, "/var/lib/foo/new", "new")
	g.file(t, "/etc/vlc/vlcrc", "the package's default")
	writeStatus(t, g.r, "Package: vlc\nStatus: install ok installed\nArchitecture: amd64\nConffiles:\n /etc/vlc/vlcrc 0123\n")
	if err := WriteRestore(g.r, RestoreJob{Op: op, Paths: []string{"/opt/Foo", "/var/lib/foo", "/usr/local/bin/foo", "/etc/vlc/vlcrc", "/etc/other"}}); err != nil {
		t.Fatal(err)
	}
	if err := RunRestore(ctx, g.r, op, fakeTar(t, "")); err != nil {
		t.Fatal(err)
	}
	res, err := ReadResults(g.r, op)
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	for _, x := range res {
		done[x.Path] = x.Done
	}
	if !done["/opt/Foo"] || done["/var/lib/foo"] || !done["/usr/local/bin/foo"] || !done["/etc/vlc/vlcrc"] || done["/etc/other"] {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(g.sys, "opt/Foo/bin/foo")); string(b) != "binary" {
		t.Error("not restored")
	}
	if l, _ := os.Readlink(filepath.Join(g.sys, "opt/Foo/foo")); l != "bin/foo" {
		t.Error("the link is not restored as a link")
	}
	if b, _ := os.ReadFile(filepath.Join(g.sys, "etc/vlc/vlcrc")); string(b) != "edited by the user" {
		t.Errorf("conffile %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(g.sys, "var/lib/foo/new")); string(b) != "new" {
		t.Error("an existing folder was touched")
	}
}

func writeStatus(t *testing.T, r Roots, s string) {
	t.Helper()
	if err := os.WriteFile(r.DpkgStatus, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A link that has the previewed folder's device and inode, as on a
// filesystem that reuses inode numbers at once, is still not the folder.
func TestArchiveRefusesAnotherKind(t *testing.T) {
	g := newRig(t)
	if err := os.Symlink("/etc", filepath.Join(g.sys, "opt/Link")); err != nil {
		t.Fatal(err)
	}
	mv := g.move(t, "/opt/Link")
	if mv.Kind != rules.EntryLink {
		t.Fatalf("kind %q", mv.Kind)
	}
	for _, kind := range []string{rules.EntryDir, rules.EntryFile, "", "socket"} {
		mv.Kind = kind
		err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{mv}})
		if err == nil {
			err = RunArchive(context.Background(), g.r, op, fakeTar(t, ""))
		}
		if err == nil {
			t.Fatalf("a link was archived as %q", kind)
		}
		if l, err := os.Readlink(filepath.Join(g.sys, "opt/Link")); err != nil || l != "/etc" {
			t.Fatalf("kind %q: the link was touched", kind)
		}
	}
}

func TestArchiveRefusesSwap(t *testing.T) {
	g := newRig(t)
	g.file(t, "/etc/foo/a.conf", "x")
	mv := g.move(t, "/etc/foo")
	// Between preview and run, /etc/foo becomes a link to /etc/shadow's folder.
	if err := os.RemoveAll(filepath.Join(g.sys, "etc/foo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(g.sys, "etc/foo")); err != nil {
		t.Fatal(err)
	}
	if err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{mv}}); err != nil {
		t.Fatal(err)
	}
	err := RunArchive(context.Background(), g.r, op, fakeTar(t, ""))
	if err == nil || !strings.Contains(err.Error(), "changed since the preview") {
		t.Fatalf("%v", err)
	}
	if l, err := os.Readlink(filepath.Join(g.sys, "etc/foo")); err != nil || l != "/etc" {
		t.Fatal("the swapped link was touched")
	}
}

func TestArchiveFailureStopsAndPutsBack(t *testing.T) {
	g := newRig(t)
	g.file(t, "/opt/A/x", "a")
	g.file(t, "/opt/B/x", "b")
	if err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{g.move(t, "/opt/A"), g.move(t, "/opt/B")}}); err != nil {
		t.Fatal(err)
	}
	if err := RunArchive(context.Background(), g.r, op, fakeTar(t, "leftovers-tar-create")); err == nil {
		t.Fatal("no error")
	}
	for _, p := range []string{"opt/A/x", "opt/B/x"} {
		if _, err := os.Stat(filepath.Join(g.sys, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(g.sys, "opt")); len(ents) != 2 {
		t.Errorf("a moved-aside name was left behind: %v", ents)
	}
}

func TestArchiveRefusesUnsafeParent(t *testing.T) {
	g := newRig(t)
	g.file(t, "/opt/A/x", "a")
	if err := os.Chmod(filepath.Join(g.sys, "opt"), 0o777); err != nil { // #nosec G302 -- a test fixture
		t.Fatal(err)
	}
	if err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{g.move(t, "/opt/A")}}); err != nil {
		t.Fatal(err)
	}
	if err := RunArchive(context.Background(), g.r, op, fakeTar(t, "")); err == nil || !strings.Contains(err.Error(), "only root") {
		t.Fatalf("%v", err)
	}
}

func TestJobIsCheckedAgain(t *testing.T) {
	g := newRig(t)
	for _, j := range []string{
		`{"op":"` + op + `","uid":1,"item":"x","moves":[{"path":"/etc/../root","dev":1,"ino":1}]}`,
		`{"op":"` + op + `","uid":1,"item":"x","moves":[{"path":"/usr/bin/x","dev":1,"ino":1}]}`,
		`{"op":"` + op + `","uid":1,"item":"x","conffiles":[{"package":"x","path":"/root/.ssh/authorized_keys"}]}`,
		`{"op":"ffffffffffffffffffffffffffffffff","uid":1,"item":"x"}`,
		`{"op":"` + op + `","uid":1,"item":"x","extra":1}`,
	} {
		if err := os.WriteFile(filepath.Join(g.r.OpDir(op), "job.json"), []byte(j), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RunArchive(context.Background(), g.r, op, fakeTar(t, "")); err == nil {
			t.Errorf("ran %s", j)
		}
	}
	if err := RunArchive(context.Background(), g.r, "../x", fakeTar(t, "")); err == nil {
		t.Error("ran a bad op")
	}
}

func TestRestoreRefusesTamperedArchive(t *testing.T) {
	g := newRig(t)
	g.file(t, "/opt/A/x", "a")
	if err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{g.move(t, "/opt/A")}}); err != nil {
		t.Fatal(err)
	}
	if err := RunArchive(context.Background(), g.r, op, fakeTar(t, "")); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(g.r.RemovedOp(op), rules.ArchiveName(0))
	if err := os.WriteFile(a, []byte(`[{"name":"../../etc/x","data":"evil"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteRestore(g.r, RestoreJob{Op: op, Paths: []string{"/opt/A"}}); err != nil {
		t.Fatal(err)
	}
	if err := RunRestore(context.Background(), g.r, op, fakeTar(t, "")); err != nil {
		t.Fatal(err)
	}
	res, _ := ReadResults(g.r, op)
	if len(res) != 1 || res[0].Done || !strings.Contains(res[0].Reason, "does not match") {
		t.Fatalf("%+v", res)
	}
}

func TestClean(t *testing.T) {
	g := newRig(t)
	g.file(t, "/opt/A/x", "a")
	if err := WriteJob(g.r, Job{Op: op, UID: 1, Item: "x", Moves: []Move{g.move(t, "/opt/A")}}); err != nil {
		t.Fatal(err)
	}
	if err := RunArchive(context.Background(), g.r, op, fakeTar(t, "")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(g.r.Removed, "not-an-op")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Clean(g.r, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.r.RemovedOp(op)); err != nil {
		t.Fatal("a fresh archive was removed")
	}
	if err := Clean(g.r, time.Now().Add(Keep+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.r.RemovedOp(op)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an old archive stayed")
	}
	if _, err := os.Stat(g.r.OpDir(op)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an old plan stayed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a folder not named as an operation was removed")
	}
}

func TestReadStatus(t *testing.T) {
	g := newRig(t)
	writeStatus(t, g.r, "Package: a\nStatus: deinstall ok config-files\nArchitecture: amd64\nPriority: optional\nConffiles:\n /etc/a/a.conf 1\n /etc/a/old 2 obsolete\n\n"+
		"Package: bash\nEssential: yes\nStatus: install ok installed\nArchitecture: amd64\n")
	pkgs, err := ReadStatus(g.r)
	if err != nil || len(pkgs) != 2 {
		t.Fatal(pkgs, err)
	}
	a := Find(pkgs, "a:amd64")
	if len(a) != 1 || !a[0].ConfigOnly() || a[0].Installed() || strings.Join(a[0].Conffiles, ",") != "/etc/a/a.conf" {
		t.Fatalf("%+v", a)
	}
	if b := Find(pkgs, "bash"); b[0].Undeletable() != "essential" {
		t.Fatal(b)
	}
}
