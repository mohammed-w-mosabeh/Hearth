// Package pathcheck resolves a stack's bind roots and bind sources and
// checks the result against S12b (ROADMAP 9.3). It runs in
// hearth-pathcheck@.service, which holds only CAP_DAC_READ_SEARCH and has
// no network, so the broker gains no capability and no view of home.
//
// The kernel resolves each path with openat2 and RESOLVE_NO_MAGICLINKS,
// opening it O_PATH: nothing is read, no device is opened, and a /proc
// link to another root is refused rather than followed. The real path is
// read back from the descriptor, so it is what the kernel reached, not a
// lexical guess. Each bind also records the mount it lies on, as device
// and path within that filesystem, so the live mounts can be compared
// after up (VerifyMounts): a link swapped between this check and the mount
// shows there.
package pathcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// SocketPath is hearth-pathcheck.socket's socket, root's only.
const SocketPath = "/run/hearth/pathcheck.sock"

// MaxRequest bounds a request line: a spec and its JSON wrapping.
const MaxRequest = spec.MaxSpec + 1024

// Request asks for one spec's binds to be checked.
type Request struct {
	Spec json.RawMessage `json:"spec"`
}

// Reply is the answer: OK, or the refusals. Roots and Binds are filled
// only when OK.
type Reply struct {
	OK      bool              `json:"ok"`
	Refused []string          `json:"refused"`
	Roots   map[string]string `json:"roots,omitempty"`
	Binds   []Resolved        `json:"binds,omitempty"`
}

// Resolved is one bind after resolution.
type Resolved struct {
	spec.Bind
	Real string `json:"real"`
	// Dev and FSRoot name the mount the source lies on, as mountinfo does:
	// the device's major:minor and the path within that filesystem.
	Dev    string `json:"dev"`
	FSRoot string `json:"fs_root"`
}

// Resolver resolves paths. Open and Mounts are replaced in tests.
type Resolver struct {
	// Open resolves p and returns its real path and mount ID.
	Open func(p string) (real string, mntID uint64, err error)
	// Mounts returns the resolver's own mountinfo.
	Mounts func() ([]byte, error)
}

// System is the resolver on the running kernel.
var System = Resolver{Open: openReal, Mounts: func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }}

func openReal(p string) (string, uint64, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, p, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return "", 0, err
	}
	if strings.HasSuffix(real, " (deleted)") {
		return "", 0, unix.ENOENT // removed while being checked
	}
	var st unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_STATX_DONT_SYNC, unix.STATX_MNT_ID, &st); err != nil {
		return "", 0, err
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		return "", 0, errors.New("the kernel gave no mount ID")
	}
	return real, st.Mnt_id, nil
}

// Check parses raw as a spec, then resolves and checks its roots and binds.
// It never reads, writes or opens anything but O_PATH descriptors.
func (r Resolver) Check(raw []byte) Reply {
	sp, rep, err := spec.Parse(raw)
	if err != nil {
		return refused(err.Error())
	}
	if !rep.OK() {
		return Reply{Refused: rep.Refused}
	}
	var mounts []mountEntry
	if b := spec.Binds(sp); len(b) > 0 {
		raw, err := r.Mounts()
		if err != nil {
			return refused("mountinfo: " + err.Error())
		}
		mounts = parseMountinfo(raw)
	}
	out := Reply{Roots: map[string]string{}}
	var why []string
	for _, root := range sp.BindRoots {
		real, _, err := r.Open(root)
		if err != nil {
			why = append(why, fmt.Sprintf("bind_roots: %s: %s", root, openErr(err)))
			continue
		}
		if w := spec.CheckRoot(real); w != "" {
			why = append(why, fmt.Sprintf("bind_roots: %s: %s", root, w))
			continue
		}
		out.Roots[root] = real
	}
	for _, b := range spec.Binds(sp) {
		real, id, err := r.Open(b.Source)
		if err != nil {
			why = append(why, fmt.Sprintf("%s: bind source %s: %s", b.Service, b.Source, openErr(err)))
			continue
		}
		if w := spec.CheckResolved(sp, out.Roots, b.Source, real); w != "" {
			why = append(why, fmt.Sprintf("%s: bind source %s: %s", b.Service, b.Source, w))
			continue
		}
		dev, fsRoot, ok := locate(mounts, id, real)
		if !ok {
			why = append(why, fmt.Sprintf("%s: bind source %s: its mount was not found", b.Service, b.Source))
			continue
		}
		out.Binds = append(out.Binds, Resolved{Bind: b, Real: real, Dev: dev, FSRoot: fsRoot})
	}
	if len(why) > 0 {
		slices.Sort(why)
		return Reply{Refused: why}
	}
	out.OK = true
	out.Refused = []string{}
	return out
}

func refused(why string) Reply { return Reply{Refused: []string{why}} }

// openErr names a resolution failure without the kernel's wording.
func openErr(err error) string {
	switch {
	case errors.Is(err, unix.ENOENT):
		return "does not exist (Hearth never creates a bind source)"
	case errors.Is(err, unix.ELOOP):
		return "passes through a /proc link to another root, or too many links"
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return "cannot be reached"
	case errors.Is(err, unix.ENOTDIR):
		return "a part of it is not a folder"
	case errors.Is(err, unix.ENAMETOOLONG):
		return "is too long"
	case errors.Is(err, unix.ENOSYS):
		return "cannot be checked: openat2 is not available"
	}
	return "cannot be resolved"
}

// Same reports whether two OK replies resolved every root and bind to the
// same place, as the second check before up must.
func Same(a, b Reply) bool {
	if !a.OK || !b.OK || len(a.Roots) != len(b.Roots) || len(a.Binds) != len(b.Binds) {
		return false
	}
	for k, v := range a.Roots {
		if b.Roots[k] != v {
			return false
		}
	}
	for i := range a.Binds {
		if a.Binds[i] != b.Binds[i] {
			return false
		}
	}
	return true
}

// VerifyMounts compares a running container's mountinfo, read from
// /proc/<pid>/mountinfo, with the binds checked for its service. It
// returns one line per bind whose live mount is not the checked one.
func VerifyMounts(mountinfo []byte, binds []Resolved) []string {
	entries := parseMountinfo(mountinfo)
	var bad []string
	for _, b := range binds {
		var found *mountEntry
		for i := range entries {
			if entries[i].point == b.Target {
				found = &entries[i] // the last one is on top
			}
		}
		switch {
		case found == nil:
			bad = append(bad, fmt.Sprintf("%s: %s is not mounted", b.Service, b.Target))
		case found.dev != b.Dev || found.root != b.FSRoot:
			bad = append(bad, fmt.Sprintf("%s: %s is not the checked %s", b.Service, b.Target, b.Real))
		}
	}
	return bad
}

// Serve answers one request read from r on w, as the unit does on its
// socket.
func Serve(res Resolver, r io.Reader, w io.Writer) error {
	line, err := bufio.NewReaderSize(io.LimitReader(r, MaxRequest+1), 64<<10).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var reply Reply
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	switch {
	case len(line) > MaxRequest:
		reply = refused("the request is too large")
	case dec.Decode(&req) != nil || dec.More() || len(req.Spec) == 0:
		reply = refused("not a path check request")
	default:
		reply = res.Check(req.Spec)
	}
	return json.NewEncoder(w).Encode(reply)
}

type mountEntry struct {
	id    uint64
	dev   string
	root  string
	point string
}

// parseMountinfo reads the fields it needs from proc(5)'s mountinfo.
func parseMountinfo(b []byte) []mountEntry {
	var out []mountEntry
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		id, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, mountEntry{id: id, dev: f[2], root: unescape(f[3]), point: unescape(f[4])})
	}
	return out
}

// locate finds the mount with ID id and returns its device and the path
// of real within that filesystem.
func locate(mounts []mountEntry, id uint64, real string) (dev, fsRoot string, ok bool) {
	for _, m := range mounts {
		if m.id != id {
			continue
		}
		rel, inside := strings.CutPrefix(real, m.point)
		if m.point == "/" {
			rel, inside = real, true
		}
		if !inside || (rel != "" && !strings.HasPrefix(rel, "/")) {
			return "", "", false
		}
		return m.dev, path.Join(m.root, rel), true
	}
	return "", "", false
}

// unescape undoes the octal escapes (\040 space, \011 tab, \012 newline,
// \134 backslash) mountinfo uses.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
