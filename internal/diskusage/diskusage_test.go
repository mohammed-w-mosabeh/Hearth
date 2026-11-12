package diskusage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tree builds files of known sizes under a temp home.
func tree(t *testing.T) (home, outside string) {
	t.Helper()
	base := t.TempDir()
	home, outside = filepath.Join(base, "home"), filepath.Join(base, "secret")
	write := func(rel string, n int) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Videos/a.mkv", 64<<10)
	write("Videos/sub/b.mkv", 32<<10)
	write("Documents/x.txt", 4<<10)
	write("big.iso", 128<<10)
	write("empty/.keep", 0)
	// A hard link is counted once.
	if err := os.Link(filepath.Join(home, "big.iso"), filepath.Join(home, "Videos/big-link.iso")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(home, "etc-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "to-secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, "locked"), 0o700) }) // #nosec G302 -- a directory needs x to be removed
	return home, outside
}

func newScanner(home string) *Scanner {
	return New(Config{Roots: func() []string { return []string{home} }})
}

func byName(r Result) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range r.Items {
		m[e.Name] = e
	}
	return m
}

// done polls path until its job finishes.
func done(t *testing.T, s *Scanner, path string, fresh bool) Result {
	t.Helper()
	r, err := s.Get(path, fresh)
	for deadline := time.Now().Add(10 * time.Second); err == nil && r.Running; {
		if time.Now().After(deadline) {
			t.Fatalf("scan of %s did not finish", path)
		}
		time.Sleep(5 * time.Millisecond)
		r, err = s.Get(path, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScan(t *testing.T) {
	home, _ := tree(t)
	s := newScanner(home)
	r := done(t, s, home, false)
	m := byName(r)
	if r.Items[0].Name != "big.iso" || m["big.iso"].Size < 128<<10 {
		t.Fatalf("order %+v", r.Items)
	}
	// Videos: two files plus a second link to big.iso, already counted at the top.
	if v := m["Videos"]; !v.Dir || !v.Nav || v.Pending || v.Files != 3 || v.Size < 96<<10 || v.Size >= 128<<10 {
		t.Errorf("Videos %+v", v)
	}
	if l := m["etc-link"]; !l.Link || l.Dir || l.Nav {
		t.Errorf("symlink followed: %+v", l)
	}
	if l := m["to-secret"]; !l.Link || l.Nav {
		t.Errorf("symlink to outside: %+v", l)
	}
	if os.Getuid() != 0 && r.Unreadable != 1 {
		t.Errorf("unreadable %d", r.Unreadable)
	}
	var sum int64
	for _, e := range r.Items {
		sum += e.Size
	}
	if r.Size != sum || r.Root != home || r.Partial || r.Done != r.Total || r.Total != 4 { // Videos, Documents, empty, locked
		t.Errorf("result %+v", r)
	}
	// Again from the cache; a subfolder was measured on the way.
	if r2, _ := s.Get(home, false); !r2.Cached {
		t.Error("not cached")
	}
	if v, _ := s.Get(filepath.Join(home, "Videos"), false); !v.Cached || len(v.Items) != 3 || byName(v)["sub"].Files != 1 {
		t.Errorf("subfolder not cached: %+v", v)
	}
	if r3, _ := s.Get(home, true); r3.Cached {
		t.Error("fresh scan came from the cache")
	}
	done(t, s, home, false)
}

func TestResolve(t *testing.T) {
	home, outside := tree(t)
	s := newScanner(home)
	for _, p := range []string{outside, filepath.Join(home, "to-secret"), filepath.Join(home, "to-secret") + "/", "/etc", "relative", filepath.Join(home, "..")} {
		if _, _, err := s.Resolve(p); err == nil {
			t.Errorf("%s allowed", p)
		}
	}
	if _, _, err := s.Resolve(filepath.Join(home, "big.iso")); err == nil {
		t.Error("a file allowed")
	}
	if p, root, err := s.Resolve(filepath.Join(home, "Videos", "..", "Videos")); err != nil || p != filepath.Join(home, "Videos") || root != home {
		t.Errorf("%s %s %v", p, root, err)
	}
	// The home itself through a symlink resolves to it.
	link := filepath.Join(filepath.Dir(home), "home-link")
	_ = os.Symlink(home, link)
	if p, _, err := s.Resolve(link); err != nil || p != home {
		t.Errorf("symlinked home %s %v", p, err)
	}
}

func TestLimits(t *testing.T) {
	home, _ := tree(t)
	s := New(Config{Roots: func() []string { return []string{home} }, MaxItems: 2})
	r := done(t, s, home, false)
	if len(r.Items) != 2 || r.Rest == nil || r.Rest.Files == 0 {
		t.Fatalf("rest %+v", r)
	}
}

func many(t *testing.T, home string, n int) string {
	t.Helper()
	dir := filepath.Join(home, "many")
	for i := range n {
		d := filepath.Join(dir, strconv.Itoa(i%50))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIdleJobStops(t *testing.T) {
	home, _ := tree(t)
	many(t, home, 20000)
	s := New(Config{Roots: func() []string { return []string{home} }, Idle: 8 * time.Millisecond, Workers: 1})
	if r, _ := s.Get(home, false); !r.Running {
		t.Skip("scan finished before it could be abandoned")
	}
	// Nobody asks: the job stops, unfinished, and is not cached.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		j := s.job
		s.mu.Unlock()
		if j == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned job kept running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := s.cache[home]; ok {
		t.Fatal("partial result cached")
	}
}

func TestOtherFolderCancelsJob(t *testing.T) {
	home, _ := tree(t)
	held := many(t, home, 100)
	// The first job's walk of "many" waits until the other folder was asked
	// for, so it can never finish before it is cancelled.
	release := make(chan struct{})
	readDir = func(p string) ([]os.DirEntry, error) {
		if strings.HasPrefix(p, held) {
			<-release
		}
		return os.ReadDir(p)
	}
	t.Cleanup(func() { readDir = os.ReadDir })
	s := New(Config{Roots: func() []string { return []string{home} }, Workers: 1})
	first, _ := s.Get(home, false)
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	r := done(t, s, filepath.Join(home, "Documents"), false)
	close(release)
	if r.Path != filepath.Join(home, "Documents") || len(r.Items) != 1 {
		t.Fatalf("%+v", r)
	}
	if !first.Running || j == nil {
		t.Fatalf("the first job was not running: %+v", first)
	}
	{
		j.mu.Lock()
		fin, partial := j.finished, j.partial
		j.mu.Unlock()
		for i := 0; i < 1000 && !fin; i++ {
			time.Sleep(5 * time.Millisecond)
			j.mu.Lock()
			fin, partial = j.finished, j.partial
			j.mu.Unlock()
		}
		if !fin || !partial {
			t.Errorf("first job finished=%v partial=%v", fin, partial)
		}
	}
}

func TestUserRoots(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "me")
	mine := filepath.Join(base, "media", "mine")
	for _, d := range []string{home, mine} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roots := UserRoots(home, func() ([]string, error) {
		return []string{"/", filepath.Join(base, "home"), mine, "/proc"}, nil
	})()
	// /proc is owned by root; the home's own mount is not listed again.
	if strings.Join(roots, ",") != home+","+mine {
		t.Fatalf("%v", roots)
	}
}

func TestHandler(t *testing.T) {
	home, outside := tree(t)
	s := newScanner(home)
	get := func(path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		s.HandleUsage(rec, httptest.NewRequest("GET", "/api/v1/disks/usage?path="+url.QueryEscape(path), nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := get(home)
	for i := 0; i < 1000 && out["running"] == true; i++ {
		time.Sleep(5 * time.Millisecond)
		code, out = get(home)
	}
	if code != 200 || out["path"] != home || out["running"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	for p, want := range map[string]int{outside: 403, "": 400, filepath.Join(home, "nope"): 404, "/etc": 403} {
		if code, _ := get(p); code != want {
			t.Errorf("%q: %d, want %d", p, code, want)
		}
	}
	rec := httptest.NewRecorder()
	s.HandleRoots(rec, httptest.NewRequest("GET", "/api/v1/disks/roots", nil))
	if !strings.Contains(rec.Body.String(), home) {
		t.Errorf("roots %s", rec.Body.String())
	}
	_ = http.StatusOK
}
