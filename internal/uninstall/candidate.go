package uninstall

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
)

// Groups of a plan, in the order the preview shows them.
const (
	GroupProgram = "program"
	GroupOrphans = "orphans"
	GroupPurge   = "purge"
	GroupRunning = "running"
	GroupUser    = "user"
	GroupSystem  = "system"
	GroupRefs    = "refs"
	GroupRemains = "remains"
)

// Kinds of candidate.
const (
	KindPackage = "package" // a dpkg package, "name:arch"
	KindFlatpak = "flatpak" // a Flatpak ref, as the listing's ID
	KindSnap    = "snap"    // a snap name
	KindExtra   = "extra"   // an npm, cargo or Go program, by the listing's ID
	KindSteam   = "steam"   // a Steam game, by app ID: Steam's own dialog
	KindTrash   = "trash"   // a user file or folder, to the trash
	KindArchive = "archive" // a root-owned file or folder, archived by the broker
	KindProcess = "process" // a running process of the user
	KindUnit    = "unit"    // a systemd unit
	KindRef     = "ref"     // one of Hearth's own references
	KindManual  = "manual"  // a command the user runs themselves (pip)
	KindNothing = "nothing" // in remains: something left, and why
)

// Candidate is one row of a plan.
type Candidate struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	ID        string `json:"id"` // unique within its group: a path, a package ID, a ref
	Name      string `json:"name"`
	Size      int64  `json:"size,omitempty"` // bytes; 0 when unknown
	Preselect bool   `json:"preselect"`
	Reason    string `json:"reason"`
	// Warn: "data" (looks like a database or mail), "desktop" (may end the
	// session), "large".
	Warn string `json:"warn,omitempty"`
	// Dev and Ino identify a path as previewed; a different file at the
	// same path is refused when the plan runs.
	Dev uint64 `json:"-"`
	Ino uint64 `json:"-"`
	// Type is the file type previewed (folder, file, link): an inode
	// number can be reused at once, so a link put where a folder was can
	// have the folder's device and inode.
	Type os.FileMode `json:"-"`
}

// pathCandidate is a Candidate for path p, with its device and inode, or
// false when p is missing. A link is never followed: the candidate is the
// link itself.
func pathCandidate(group, kind, p, reason string, pre bool) (Candidate, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return Candidate{}, false
	}
	c := Candidate{Group: group, Kind: kind, ID: p, Name: p, Preselect: pre, Reason: reason}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		c.Dev, c.Ino = st.Dev, st.Ino
	}
	c.Type = fi.Mode().Type()
	if fi.Mode().IsRegular() {
		c.Size = fi.Size()
	}
	return c, true
}

// owner is the uid owning p (not following a link), and whether p's
// folder is root-owned and writable by root alone.
func owner(p string) (uid uint32, safeParent bool, err error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false, os.ErrInvalid
	}
	pfi, err := os.Lstat(filepath.Dir(p))
	if err != nil {
		return st.Uid, false, nil
	}
	pst, ok := pfi.Sys().(*syscall.Stat_t)
	safe := ok && pfi.IsDir() && rootOwned(pst.Uid) && pfi.Mode().Perm()&0o022 == 0
	return st.Uid, safe, nil
}

// overflowUID is the owner an unmapped uid shows as inside a user
// namespace. systemd puts a sandboxed user service (hearthd) in one, so
// there root's files show as owned by it; hearthd cannot tell root from
// another unmapped user, and the broker checks the real owner again.
var overflowUID = func() uint32 {
	b, err := os.ReadFile("/proc/sys/kernel/overflowuid")
	if err != nil {
		return 65534
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
	if err != nil {
		return 65534
	}
	return uint32(n)
}()

// rootOwned reports whether uid is root as hearthd can see it.
func rootOwned(uid uint32) bool {
	return uid == archive.RootUID || (archive.RootUID == 0 && uid == overflowUID)
}
