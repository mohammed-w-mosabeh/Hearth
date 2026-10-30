package space

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fixture struct {
	a       *Advisor
	home    string
	runs    [][]string
	runErr  error
	calls   []string
	callErr error
	reg     *actions.Registry
}

func (f *fixture) Call(_ context.Context, method string, _, _ any) error {
	f.calls = append(f.calls, method)
	return f.callErr
}

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *fixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gio := filepath.Join(home, "fake-gio")
	write(t, gio, 0)
	f := &fixture{home: home}
	f.a = New(Config{Home: home, Archives: filepath.Join(home, "archives"), Journal: filepath.Join(home, "journal"), Gio: gio, Broker: f,
		Run: func(_ context.Context, _, _ string, argv, _ []string) (string, error) {
			f.runs = append(f.runs, argv)
			return "", f.runErr
		}})
	f.reg = actions.NewRegistry(audit{})
	f.a.Register(f.reg)
	return f
}

type audit struct{}

func (audit) Audit(context.Context, store.AuditEntry) error { return nil }

func (f *fixture) plan(t *testing.T, name, target string, params map[string]string) (actions.Action, actions.Request, actions.Plan) {
	t.Helper()
	a, ok := f.reg.Lookup("space/" + name)
	if !ok {
		t.Fatal(name)
	}
	r := actions.Request{Target: target, Params: params}
	if err := a.Validate(r); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return a, r, p
}

func TestReportMeasures(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.home, "archives/a.deb"), 8192)
	write(t, filepath.Join(f.home, "archives/lock"), 8192)
	write(t, filepath.Join(f.home, "journal/x/system.journal"), 8192)
	write(t, filepath.Join(f.home, ".cache/big/a/b"), 1<<20)
	write(t, filepath.Join(f.home, ".cache/small"), 4096)
	write(t, filepath.Join(f.home, ".local/share/Trash/files/old"), 4096)
	if err := os.Symlink("/etc", filepath.Join(f.home, ".cache/link")); err != nil {
		t.Fatal(err)
	}
	r := f.a.Report(context.Background())
	if r.Apt.Count != 1 || r.Journal.Size == 0 || r.Cache.Count != 2 || r.Cache.Items[0].Name != "big" || r.Trash.Count != 1 || !r.CanTrash {
		t.Fatalf("%+v", r)
	}
	for _, e := range r.Cache.Items {
		if e.Name == "link" {
			t.Error("a link was listed")
		}
	}
}

func pkg(name string, kib int64) discovery.Package {
	return discovery.Package{Name: name, Arch: "amd64", State: "installed", InstalledKiB: kib}
}

func TestOldKernels(t *testing.T) {
	const v = "+deb13-amd64"
	list := []discovery.Package{
		pkg("linux-image-6.12.9"+v, 100), pkg("linux-image-6.12.10"+v, 100), pkg("linux-image-6.12.101"+v, 100),
		pkg("linux-image-6.12.111"+v, 100), pkg("linux-image-6.12.113"+v, 100),
		pkg("linux-image-amd64", 1), pkg("linux-headers-amd64", 1),
		pkg("linux-headers-6.12.9"+v, 10), pkg("linux-headers-6.12.9+deb13-common", 10), pkg("linux-kbuild-6.12.9+deb13", 10),
		pkg("linux-headers-6.12.113"+v, 10), pkg("linux-libc-dev", 10), pkg("vim", 10),
	}
	ids := func(ks []Kernel) []string {
		out := []string{}
		for _, k := range ks {
			out = append(out, strings.TrimSuffix(k.Package, ":amd64"))
		}
		slices.Sort(out)
		return out
	}
	got := ids(oldKernels(list, "6.12.113"+v))
	want := []string{"linux-headers-6.12.9+deb13-amd64", "linux-headers-6.12.9+deb13-common", "linux-image-6.12.10+deb13-amd64",
		"linux-image-6.12.101+deb13-amd64", "linux-image-6.12.9+deb13-amd64", "linux-kbuild-6.12.9+deb13"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	// Booted into an old one: it stays, and so do the two newest.
	got = ids(oldKernels(list, "6.12.9"+v))
	if slices.ContainsFunc(got, func(s string) bool {
		return strings.Contains(s, "6.12.9+") || strings.Contains(s, ".113") || strings.Contains(s, ".111")
	}) {
		t.Fatalf("%v", got)
	}
	if got := oldKernels(list, ""); len(got) != 0 {
		t.Errorf("no running kernel known: %v", got)
	}
	if got := oldKernels(list[3:5], "other"); len(got) != 0 {
		t.Errorf("two kernels: %v", got)
	}
}

func TestLargeFiles(t *testing.T) {
	f := setup(t)
	big := func(p string) { t.Helper(); write(t, p, minLarge+4096) }
	big(filepath.Join(f.home, "Videos/a.mkv"))
	big(filepath.Join(f.home, ".local/share/Steam/game.bin"))
	big(filepath.Join(f.home, "Videos/.hidden/b.mkv"))
	write(t, filepath.Join(f.home, "small"), 4096)
	res, err := f.a.Large(context.Background())
	if err != nil || len(res.Items) != 1 || res.Items[0].Name != "a.mkv" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestTrashChecks(t *testing.T) {
	f := setup(t)
	cacheDir := filepath.Join(f.home, ".cache/thumbnails")
	write(t, filepath.Join(cacheDir, "x"), 10)
	file := filepath.Join(f.home, "Videos/a.mkv")
	write(t, file, 10)
	write(t, filepath.Join(f.home, ".ssh/id_ed25519"), 10)
	if err := os.Symlink(filepath.Join(f.home, ".ssh"), filepath.Join(f.home, "Videos/keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.home, ".ssh"), filepath.Join(f.home, ".cache/keys")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		target, path string
		ok           bool
	}{
		{"cache", cacheDir, true},
		{"large", file, true},
		{"cache", filepath.Join(f.home, ".cache"), false},
		{"cache", filepath.Join(cacheDir, "x"), false},
		{"cache", file, false},
		{"cache", filepath.Join(f.home, ".cache/keys"), false},
		{"large", f.home, false},
		{"large", filepath.Join(f.home, "Videos"), false},
		{"large", filepath.Join(f.home, ".ssh/id_ed25519"), false},
		{"large", filepath.Join(f.home, "Videos/keys/id_ed25519"), false},
		{"large", filepath.Join(f.home, "Videos/keys"), false},
		{"large", "/etc/passwd", false},
		{"large", filepath.Join(f.home, "Videos/../.ssh/id_ed25519"), false},
		{"other", file, false},
	} {
		if _, _, err := f.a.check(c.target, c.path); (err == nil) != c.ok {
			t.Errorf("%s %s: %v", c.target, c.path, err)
		}
	}
}

func TestTrashValidate(t *testing.T) {
	f := setup(t)
	a, _ := f.reg.Lookup("space/trash")
	many := map[string]string{}
	for i := range maxSelected + 1 {
		many[string(rune('a'+i))] = "/a"
	}
	for _, r := range []actions.Request{
		{Target: "etc", Params: map[string]string{"a": "/a"}},
		{Target: "cache"},
		{Target: "cache", Params: many},
		{Target: "cache", Params: map[string]string{"a": "relative"}},
		{Target: "cache", Params: map[string]string{"a": "/a/../b"}},
	} {
		if a.Validate(r) == nil {
			t.Errorf("%+v accepted", r)
		}
	}
}

func TestTrashRuns(t *testing.T) {
	f := setup(t)
	dir := filepath.Join(f.home, ".cache/thumbnails")
	write(t, filepath.Join(dir, "x"), 10)
	a, r, p := f.plan(t, "trash", "cache", map[string]string{"a": dir})
	if len(p.Items) != 1 || p.Items[0].Detail == "" {
		t.Fatalf("%+v", p)
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if len(f.runs) != 1 || !slices.Equal(f.runs[0], []string{"gio", "trash", "--", dir}) {
		t.Fatalf("%v", f.runs)
	}
}

// What the preview showed is what goes: not whatever has the path now.
func TestTrashSeesSwappedPath(t *testing.T) {
	f := setup(t)
	file := filepath.Join(f.home, "Videos/a.mkv")
	write(t, file, 10)
	a, r, p := f.plan(t, "trash", "large", map[string]string{"a": file})
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.home, "other"), 10) // keep the inode number busy
	write(t, file, 20)
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonChanged || len(f.runs) != 0 {
		t.Fatalf("%v %+v %v", err, res, f.runs)
	}
}

func TestTrashBlocksInPlan(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.home, ".ssh/key"), 10)
	_, _, p := f.plan(t, "trash", "large", map[string]string{"a": filepath.Join(f.home, ".ssh/key")})
	if len(p.Items) != 0 || len(p.Blocked) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestTrashReportsWhatGioDid(t *testing.T) {
	f := setup(t)
	one, two := filepath.Join(f.home, "a"), filepath.Join(f.home, "b")
	write(t, one, 10)
	write(t, two, 10)
	a, r, p := f.plan(t, "trash", "large", map[string]string{"a": one, "b": two})
	f.a.cfg.Run = func(context.Context, string, string, []string, []string) (string, error) {
		_ = os.Remove(one)
		return "", errors.New("gio: no room")
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 1 || len(res.Failed) != 1 || res.Failed[0].ID != two {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestEmptyTrash(t *testing.T) {
	f := setup(t)
	if _, _, p := f.plan(t, "empty-trash", "trash", nil); len(p.Items) != 0 || p.Confirm != "" {
		t.Fatalf("empty: %+v", p)
	}
	write(t, filepath.Join(f.home, ".local/share/Trash/files/old"), 10)
	a, r, p := f.plan(t, "empty-trash", "trash", nil)
	if p.Confirm == "" {
		t.Fatal("deleting for good needs the typed word")
	}
	if res, err := a.Run(context.Background(), r, p); err != nil || res.Done != 1 || !slices.Equal(f.runs[0], []string{"gio", "trash", "--empty"}) {
		t.Fatalf("%v %+v %v", err, res, f.runs)
	}
}

func TestBrokerCleanups(t *testing.T) {
	f := setup(t)
	if _, _, p := f.plan(t, "apt-clean", "apt", nil); len(p.Items) != 0 {
		t.Fatalf("nothing to clean: %+v", p)
	}
	write(t, filepath.Join(f.home, "archives/a.deb"), 10)
	a, r, p := f.plan(t, "apt-clean", "apt", nil)
	if res, err := a.Run(context.Background(), r, p); err != nil || res.Done != 1 || f.calls[0] != "apt.clean" {
		t.Fatalf("%v %+v %v", err, res, f.calls)
	}
	f.callErr = broker.Errorf(broker.CodeUnauthorized, "no")
	if res, err := a.Run(context.Background(), r, p); err != nil || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnauthorized {
		t.Fatalf("%v %+v", err, res)
	}
	f.callErr = broker.Errorf(broker.CodeConflict, "apt is running")
	if res, _ := a.Run(context.Background(), r, p); len(res.Failed) != 1 || res.Failed[0].Detail != "apt is running" {
		t.Fatalf("%+v", res)
	}
	f.callErr = errors.New("no socket")
	if _, err := a.Run(context.Background(), r, p); err == nil {
		t.Fatal("a missing broker is an error")
	}
	f.callErr = nil

	// Already smaller than what would be kept: nothing to do.
	write(t, filepath.Join(f.home, "journal/x"), 4096)
	if _, _, p := f.plan(t, "journal-vacuum", "100", nil); len(p.Items) != 0 {
		t.Fatalf("%+v", p)
	}
	j, _ := f.reg.Lookup("space/journal-vacuum")
	for _, target := range []string{"0", "1", "99", "-100", "x", "100M"} {
		if j.Validate(actions.Request{Target: target}) == nil {
			t.Errorf("%s accepted", target)
		}
	}
}

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func TestHandlers(t *testing.T) {
	f := setup(t)
	m := mux{http.NewServeMux()}
	f.a.HandleRegister(m)
	for _, target := range []string{"/api/v1/space", "/api/v1/space/large"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}
}
