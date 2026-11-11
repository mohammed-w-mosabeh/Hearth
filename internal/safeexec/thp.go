package safeexec

import (
	"os"
	"os/exec"
	"sync"

	"golang.org/x/sys/unix"
)

// thpMark tells the image started by NoHugePages that the flag it finds
// set is hearthd's own, not one it inherited.
const thpMark = "HEARTH_THP_OFF"

var (
	thpMu  sync.Mutex
	thpOff bool // this process turned huge pages off for itself
)

// NoHugePages turns transparent huge pages off for the process and starts
// it again, once, so that the new image never has any: the same file
// (/proc/self/exe, even when the package was upgraded underneath), the
// same arguments, and the same environment plus a mark that the new image
// removes. Nothing is the caller's to choose, so there is nothing to
// validate (SECURITY S7). Call it in main before anything that must not
// happen twice.
//
// Where the kernel's setting is "always", memory becomes resident 2 MB at
// a time: the Go heap's arenas, and the binary's zeroed data when a
// variable the runtime writes falls in a 2 MB-aligned block of it. Idle,
// that was 14 MB of huge pages for a 5 MB live heap, and a binary 0.6 MB
// larger cost 4 MB of RSS (Gate 12). A daemon that sleeps has nothing to
// gain from them.
//
// The flag is kept across execve and by children. The tools Run starts are
// short-lived and keep it; a program Start leaves running (the user's
// browser) gets the system's setting back, and the terminal's shell and
// launched apps are started by systemd, not by this process. A flag found
// set at the start was set by whoever started us and is left as it is, for
// children too. Every failure is ignored: the process then runs as it did.
func NoHugePages() {
	if os.Getenv(thpMark) != "" {
		_ = os.Unsetenv(thpMark)
		off, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0)
		setTHPOff(err == nil && off != 0)
		return
	}
	if off, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0); err != nil || off != 0 {
		return
	}
	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0); err != nil {
		return
	}
	// #nosec G204 -- the process itself, its own arguments
	_ = unix.Exec("/proc/self/exe", os.Args, append(os.Environ(), thpMark+"=1"))
	// Still the old image: its huge pages stay, new memory has none.
	setTHPOff(true)
}

func setTHPOff(off bool) {
	thpMu.Lock()
	thpOff = off
	thpMu.Unlock()
}

// startDetached starts a program that outlives us with the system's huge
// page setting instead of ours. The flag belongs to the whole process and
// the new program takes it at its execve, which Start waits for, so it is
// cleared for that moment only; one start at a time.
func startDetached(cmd *exec.Cmd) error {
	thpMu.Lock()
	defer thpMu.Unlock()
	if !thpOff {
		return cmd.Start()
	}
	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 0, 0, 0, 0); err != nil {
		return cmd.Start()
	}
	err := cmd.Start()
	_ = unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0)
	return err
}
