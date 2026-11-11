package safeexec

import (
	"bytes"
	"os/exec"
	"regexp"
	"testing"

	"golang.org/x/sys/unix"
)

var thpLine = regexp.MustCompile(`(?m)^THP_enabled:\s*(\d)$`)

// childTHP starts cat on its own status with start and returns the
// kernel's THP_enabled for it.
func childTHP(t *testing.T, start func(*exec.Cmd) error) string {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command("/usr/bin/cat", "/proc/self/status")
	cmd.Stdout = &out
	if err := start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	m := thpLine.FindSubmatch(out.Bytes())
	if m == nil {
		t.Skip("this kernel does not report THP_enabled")
	}
	return string(m[1])
}

// TestStartedProgramKeepsTheSystemsHugePages: with huge pages off for
// hearthd, a tool it runs inherits that, and a program it leaves running
// does not; hearthd's own flag is back afterwards.
func TestStartedProgramKeepsTheSystemsHugePages(t *testing.T) {
	was, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0)
	if err != nil {
		t.Skip(err)
	}
	if was != 0 {
		t.Skip("huge pages are already off for the test")
	}
	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0); err != nil {
		t.Skip(err)
	}
	setTHPOff(true)
	t.Cleanup(func() {
		setTHPOff(false)
		_ = unix.Prctl(unix.PR_SET_THP_DISABLE, 0, 0, 0, 0)
	})

	if got := childTHP(t, (*exec.Cmd).Start); got != "0" {
		t.Fatalf("a plain child has THP_enabled %s, want 0", got)
	}
	if got := childTHP(t, startDetached); got != "1" {
		t.Fatalf("a started program has THP_enabled %s, want 1", got)
	}
	if off, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0); err != nil || off != 1 {
		t.Fatalf("our own flag after a start: %d, %v", off, err)
	}

	// A flag we did not set is not ours to lift.
	setTHPOff(false)
	if got := childTHP(t, startDetached); got != "0" {
		t.Fatalf("an inherited flag was lifted: THP_enabled %s", got)
	}
}
