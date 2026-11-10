package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const quiet = "name = \"Quiet\"\n[session]\ndo_not_disturb = true\n"

type fakeStore struct {
	mu       sync.Mutex
	armings  []store.ModeArming
	active   []store.ActiveMode
	disarmed []string
}

func (f *fakeStore) DisarmModeStale(_ context.Context, id, hash string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []store.ModeArming
	var n int64
	for _, a := range f.armings {
		if a.ModeID == id && a.FileHash != hash {
			n++
			f.disarmed = append(f.disarmed, id)
			continue
		}
		keep = append(keep, a)
	}
	f.armings = keep
	return n, nil
}

func (f *fakeStore) ModeNumbers(_ context.Context, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for i, id := range ids {
		if _, ok := out[id]; !ok {
			out[id] = int64(i + 1)
		}
	}
	return out, nil
}

func (f *fakeStore) ModeArmings(context.Context) ([]store.ModeArming, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.ModeArming{}, f.armings...), nil
}

func (f *fakeStore) ActiveModes(context.Context) ([]store.ActiveMode, error) {
	return append([]store.ActiveMode{}, f.active...), nil
}

func write(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func errorFor(errs []FileError, file string) string {
	for _, e := range errs {
		if e.File == file {
			return e.Error
		}
	}
	return ""
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "work.toml", work)
	write(t, dir, "quiet.toml", quiet)
	write(t, dir, "battery.toml", "name = \"Battery\"\ncommand = \"x\"\n")
	write(t, dir, "Bad Name.toml", quiet)
	write(t, dir, "zdupe.toml", "name = \"quiet\"\n[session]\nbrightness = 5\n")
	write(t, dir, ".work.toml.swp", "junk")
	write(t, dir, "work.toml~", "junk")
	write(t, dir, "notes.txt", "junk")
	write(t, dir, "big.toml", "name = \"Big\"\n#"+strings.Repeat("a", MaxFileSize))
	if err := os.Symlink(filepath.Join(dir, "quiet.toml"), filepath.Join(dir, "link.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(filepath.Join(dir, "fifo.toml")); err != nil {
		t.Fatal(err)
	}
	s := NewSet(Config{Dir: dir})
	if !s.Load(context.Background()) {
		t.Fatal("first load changed nothing")
	}
	modes := s.Modes()
	if len(modes) != 2 || modes[0].ID != "quiet" || modes[1].ID != "work" {
		t.Fatalf("%+v", modes)
	}
	errs := s.Errors()
	want := map[string]string{
		"battery.toml":  `unknown key "command"`,
		"Bad Name.toml": "a mode file is named",
		"zdupe.toml":    `quiet.toml already has the name "quiet"`,
		"big.toml":      "larger than",
		"link.toml":     "not a regular file",
		"folder.toml":   "not a regular file",
		"fifo.toml":     "not a regular file",
	}
	if len(errs) != len(want) {
		t.Fatalf("%+v", errs)
	}
	for f, w := range want {
		if got := errorFor(errs, f); !strings.Contains(got, w) {
			t.Errorf("%s: %q, want %q", f, got, w)
		}
	}
	if s.Load(context.Background()) {
		t.Fatal("a second load with nothing changed reported a change")
	}
	if _, ok := s.Mode("battery"); ok {
		t.Fatal("an invalid mode is listed")
	}
}

func TestLoadMissingFolder(t *testing.T) {
	s := NewSet(Config{Dir: filepath.Join(t.TempDir(), "none")})
	s.Load(context.Background())
	if len(s.Modes()) != 0 || len(s.Errors()) != 0 {
		t.Fatalf("%+v %+v", s.Modes(), s.Errors())
	}
}

func TestLoadTooMany(t *testing.T) {
	dir := t.TempDir()
	for i := range maxFiles + 2 {
		write(t, dir, fmt.Sprintf("m%03d.toml", i), fmt.Sprintf("name = \"M%d\"\n[session]\nbrightness = 5\n", i))
	}
	s := NewSet(Config{Dir: dir})
	s.Load(context.Background())
	if len(s.Modes()) != maxFiles || len(s.Errors()) != 2 {
		t.Fatalf("%d %+v", len(s.Modes()), s.Errors())
	}
}

// A file's control characters in its name are not shown.
func TestLoadCleansFileNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a\x1b[2Jb.toml", quiet)
	s := NewSet(Config{Dir: dir})
	s.Load(context.Background())
	errs := s.Errors()
	if len(errs) != 1 || strings.ContainsRune(errs[0].File, 0x1b) {
		t.Fatalf("%q", errs)
	}
}

func TestLoadCheck(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "work.toml", work)
	write(t, dir, "quiet.toml", quiet)
	var got []Item
	calls, games := 0, 0
	s := NewSet(Config{Dir: dir, Check: func(items []Item, game bool) error {
		calls++
		got = items
		if game {
			games++
		}
		for _, it := range items {
			if it.Kind == KindUnit {
				return errors.New("user/syncnotes.service: a profile cannot stop this unit")
			}
		}
		return nil
	}})
	s.Load(context.Background())
	// Both are checked; only work.toml's unit is refused. got is the last.
	if calls != 2 || games != 1 || len(got) != 9 || len(s.Modes()) != 1 || !strings.Contains(errorFor(s.Errors(), "work.toml"), "cannot stop") {
		t.Fatalf("%d %+v %+v", calls, got, s.Errors())
	}
}

// A changed, broken or removed file disarms its triggers; an unchanged one
// keeps them.
func TestLoadDisarms(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "work.toml", work)
	write(t, dir, "quiet.toml", quiet)
	write(t, dir, "gone.toml", "name = \"Gone\"\n[session]\nbrightness = 5\n")
	db := &fakeStore{}
	s := NewSet(Config{Dir: dir, Store: db})
	ctx := context.Background()
	s.Load(ctx)
	for _, m := range s.Modes() {
		db.armings = append(db.armings, store.ModeArming{ModeID: m.ID, Trigger: 0, FileHash: m.Hash})
	}
	write(t, dir, "work.toml", work+"\n# edited\n")
	write(t, dir, "quiet.toml", quiet+"bogus = 1\n")
	if err := os.Remove(filepath.Join(dir, "gone.toml")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "keep.toml", "name = \"Keep\"\n[session]\nbrightness = 9\n")
	s.Load(ctx)
	keep, _ := s.Mode("keep")
	db.armings = append(db.armings, store.ModeArming{ModeID: "keep", FileHash: keep.Hash})
	s.Load(ctx)
	if len(db.armings) != 1 || db.armings[0].ModeID != "keep" || len(db.disarmed) != 3 {
		t.Fatalf("%+v %v", db.armings, db.disarmed)
	}
}

func TestRunReloads(t *testing.T) {
	old := debounceDelay
	debounceDelay = 20 * time.Millisecond
	t.Cleanup(func() { debounceDelay = old })
	dir := filepath.Join(t.TempDir(), "modes")
	changed := make(chan struct{}, 16)
	s := NewSet(Config{Dir: dir, Changed: func() { changed <- struct{}{} }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool {
		fi, err := os.Stat(dir)
		return err == nil && fi.Mode().Perm() == 0o700
	})
	time.Sleep(50 * time.Millisecond) // the watch is in place
	// Rapid rewrites settle on the last version.
	for i := range 20 {
		write(t, dir, "quiet.toml", fmt.Sprintf("name = \"Quiet %d\"\n[session]\nbrightness = 5\n", i))
	}
	waitFor(t, func() bool {
		m, ok := s.Mode("quiet")
		return ok && m.Name == "Quiet 19"
	})
	write(t, dir, "quiet.toml", "broken")
	waitFor(t, func() bool { return len(s.Modes()) == 0 && len(s.Errors()) == 1 })
	select {
	case <-changed:
	default:
		t.Fatal("Changed was not called")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "work.toml", work)
	write(t, dir, "bad.toml", "name = \"<img src=x onerror=alert(1)>\"\nexec = 1\n")
	s := NewSet(Config{Dir: dir})
	s.Load(context.Background())
	m, _ := s.Mode("work")
	db := &fakeStore{
		armings: []store.ModeArming{
			{ModeID: "work", Trigger: 1, FileHash: m.Hash},
			{ModeID: "work", Trigger: 0, FileHash: "old"}, // another version of the file
			{ModeID: "work", Trigger: 99, FileHash: m.Hash},
		},
		active: []store.ActiveMode{{ModeID: "work", FileHash: "old", Since: time.UnixMilli(1793359297763), By: "user"}},
	}
	mux := http.NewServeMux()
	s.HandleRegister(mux, db, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/modes", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var out List
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Modes) != 1 || len(out.Modes[0].Armed) != 1 || out.Modes[0].Armed[0] != 1 || len(out.Errors) != 1 ||
		len(out.Active) != 1 || !out.Active[0].Changed || out.Active[0].ID != "work" || out.Modes[0].Number != 1 || out.Active[0].Number != 1 {
		t.Fatalf("%s", rec.Body.String())
	}
}
