package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestCleanArchives(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.deb", "b.deb", "lock", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "partial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.deb"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "keep.deb")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.deb")); err != nil {
		t.Fatal(err)
	}
	res, err := cleanArchives(dir)
	if err != nil || res.Removed != 2 || res.Freed != 10 {
		t.Fatalf("%v %+v", err, res)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if !slices.Equal(left, []string{"dir.deb", "link.deb", "lock", "notes.txt", "partial"}) {
		t.Errorf("%v", left)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a link was followed: %v", err)
	}
}

// A method's Check refuses before polkit is asked.
func TestSpaceMethodChecks(t *testing.T) {
	var ran [][]string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		ran = append(ran, append([]string{name}, args...))
		return safeexec.Result{}, nil
	}
	ms := map[string]Method{}
	for _, m := range SpaceMethods(run, t.TempDir()) {
		ms[m.Name] = m
		if !m.Privileged || m.Action != ActionSpaceClean {
			t.Errorf("%s is not behind polkit", m.Name)
		}
	}
	ctx := context.Background()
	for _, p := range []string{`{"keep_mib":0}`, `{"keep_mib":1}`, `{"keep_mib":-100}`, `{"keep_mib":"100"}`, `{"keep_mib":100,"x":1}`, `{}`} {
		if err := ms["journal.vacuum"].Check(ctx, json.RawMessage(p)); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if err := ms["apt.clean"].Check(ctx, json.RawMessage(`{"dir":"/etc"}`)); err == nil {
		t.Error("apt.clean took a parameter")
	}
	p := json.RawMessage(`{"keep_mib":200}`)
	if err := ms["journal.vacuum"].Check(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := ms["journal.vacuum"].Handle(ctx, Caller{}, p); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || !slices.Equal(ran[0], []string{"journal-vacuum", "--vacuum-size=200M"}) {
		t.Errorf("%v", ran)
	}
	if err := safeexec.Check("journal-vacuum", "--vacuum-size=200M"); err != nil {
		t.Errorf("not allowlisted: %v", err)
	}
	for _, bad := range []string{"--vacuum-size=1M", "--vacuum-time=1s", "--flush"} {
		if safeexec.Check("journal-vacuum", bad) == nil {
			t.Errorf("%s allowlisted", bad)
		}
	}
}

// fOFDSetLk is F_OFD_SETLK: unlike apt's fcntl lock it belongs to the open
// file, so it conflicts within one process too, as apt's does across two.
const fOFDSetLk = 37

func TestCleanArchivesRespectsAptsLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	deb := filepath.Join(dir, "a.deb")
	for _, p := range []string{lock, deb} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.OpenFile(lock, os.O_RDWR, 0) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	fl := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(held.Fd(), fOFDSetLk, &fl); err != nil {
		t.Fatal(err)
	}
	_, err = cleanArchives(dir)
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeConflict {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(deb); err != nil {
		t.Error("removed while apt held the lock")
	}
}
