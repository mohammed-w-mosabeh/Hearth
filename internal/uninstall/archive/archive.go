// Package archive is the root side of "Uninstall completely" (SECURITY
// S16k, S16l): the jobs the broker writes for its units, the archive and
// restore steps those units run, dpkg's status as root reads it, and the
// daily clean-up. It imports nothing of hearthd's, so the broker stays
// small.
package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"
)

// Folders, overridable for tests.
const (
	UninstallDir = "/var/lib/hearth/uninstall" // authorized plans and jobs, one folder per operation
	DpkgStatus   = "/var/lib/dpkg/status"
	DpkgInfo     = "/var/lib/dpkg/info"
	// Keep is how long archives are kept; plans go after a day.
	Keep     = 7 * 24 * time.Hour
	keepPlan = 24 * time.Hour
)

// Roots are the folders the steps use; zero values are the real ones.
type Roots struct {
	Uninstall, Removed, DpkgStatus, DpkgInfo string
	// System replaces "/" for the leftovers' own paths (tests only).
	System string
}

func (r Roots) uninstall() string { return or(r.Uninstall, UninstallDir) }
func (r Roots) removed() string   { return or(r.Removed, rules.RemovedDir) }
func (r Roots) status() string    { return or(r.DpkgStatus, DpkgStatus) }
func (r Roots) info() string      { return or(r.DpkgInfo, DpkgInfo) }

// sys maps a leftover's real path to where it is on this machine.
func (r Roots) sys(p string) string {
	if r.System == "" {
		return p
	}
	return filepath.Join(r.System, p)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// OpDir is an operation's folder of plans and jobs.
func (r Roots) OpDir(op string) string { return filepath.Join(r.uninstall(), op) }

// RemovedOp is an operation's folder of archives.
func (r Roots) RemovedOp(op string) string { return filepath.Join(r.removed(), op) }

// Move is a leftover to move into the archive, as previewed.
//
// Kind is the file type previewed (rules.EntryDir, EntryFile or EntryLink).
// Device and inode alone do not identify a path: a filesystem can give a
// removed folder's inode number at once to a link made in its place.
type Move struct {
	Path string `json:"path"`
	Dev  uint64 `json:"dev"`
	Ino  uint64 `json:"ino"`
	Kind string `json:"kind"`
}

// Kind is the kind of leftover a file of this mode is, or "" for what is
// never archived (devices, sockets, pipes).
func Kind(m fs.FileMode) string {
	switch m.Type() {
	case fs.ModeDir:
		return rules.EntryDir
	case 0:
		return rules.EntryFile
	case fs.ModeSymlink:
		return rules.EntryLink
	}
	return ""
}

// Same reports whether fi is still the leftover mv describes: the same
// device, inode and kind of file.
func (mv Move) Same(fi fs.FileInfo) bool {
	st, ok := stat(fi)
	return ok && mv.Kind != "" && st.Dev == mv.Dev && st.Ino == mv.Ino && Kind(fi.Mode()) == mv.Kind
}

// Conffile is a package's configuration file to copy.
type Conffile struct {
	Package string `json:"package"`
	Path    string `json:"path"`
}

// Job is what one archive run does: copy conffiles, then move leftovers.
type Job struct {
	Op        string     `json:"op"`
	UID       uint32     `json:"uid"`
	Item      string     `json:"item"`
	Conffiles []Conffile `json:"conffiles,omitempty"`
	Moves     []Move     `json:"moves,omitempty"`
}

// RestoreJob is what one restore run puts back.
type RestoreJob struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
}

// RestoreResult is one path's outcome.
type RestoreResult struct {
	Path   string `json:"path"`
	Done   bool   `json:"done"`
	Reason string `json:"reason,omitempty"`
}

// Tar runs one of the leftovers-tar-* commands (safeexec in production).
type Tar func(ctx context.Context, name string, args []string) (stdout []byte, truncated bool, err error)

const (
	jobFile     = "job.json"
	restoreFile = "restore.json"
	resultFile  = "result.json"
	manifest    = "manifest.json"
	maxJob      = 1 << 20
	maxConf     = 1 << 20
)

// writeFile writes b to dir/name atomically, 0600, refusing a link in
// the way.
func writeFile(dir, name string, b []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed name in a 0700 folder
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return errors.Join(werr, serr, cerr)
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// readFile reads dir/name, a regular file, bounded, not through a link.
func readFile(dir, name string, max int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, fmt.Errorf("%s is not a small regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, max))
}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

// WriteJob writes the archive job of op.
func WriteJob(r Roots, j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeFile(r.OpDir(j.Op), jobFile, b)
}

// readJob reads op's job and checks it again: the unit trusts nothing it
// did not check itself.
func readJob(r Roots, op string) (Job, error) {
	var j Job
	if !rules.ValidOp(op) {
		return j, errors.New("bad operation ID")
	}
	b, err := readFile(r.OpDir(op), jobFile, maxJob)
	if err != nil {
		return j, err
	}
	if err := decodeStrict(b, &j); err != nil {
		return j, fmt.Errorf("job: %w", err)
	}
	if j.Op != op || len(j.Moves) > rules.MaxMoved || len(j.Conffiles) > rules.MaxEntries || len(j.Item) > 300 {
		return j, errors.New("job: bad header")
	}
	for _, m := range j.Moves {
		if _, _, err := rules.CheckSystem(m.Path); err != nil {
			return j, err
		}
		if m.Kind != rules.EntryDir && m.Kind != rules.EntryFile && m.Kind != rules.EntryLink {
			return j, fmt.Errorf("job: bad kind for %q", m.Path)
		}
	}
	for _, c := range j.Conffiles {
		if err := rules.CheckConffile(c.Path); err != nil || !rules.ValidPackage(c.Package) {
			return j, fmt.Errorf("job: bad conffile %q", c.Path)
		}
	}
	return j, nil
}

// ReadManifest reads and checks op's manifest; ok is false when there is
// none yet.
func ReadManifest(r Roots, op string) (m rules.Manifest, ok bool, err error) {
	if !rules.ValidOp(op) {
		return m, false, errors.New("bad operation ID")
	}
	b, err := readFile(r.RemovedOp(op), manifest, rules.MaxManifest)
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m, err = rules.ParseManifest(op, b)
	return m, err == nil, err
}

func writeManifest(r Roots, m rules.Manifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFile(r.RemovedOp(m.Op), manifest, b)
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p) // #nosec G304 -- an archive in the 0700 removed folder
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func stat(fi fs.FileInfo) (*syscall.Stat_t, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

// RootUID is the owner a leftover's folder must have: root. Tests set
// their own uid; nothing else changes it.
var RootUID uint32

// SafeParent checks that a leftover's folder is root's and only root can
// write it, so nothing else can swap what is in it.
func SafeParent(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := stat(fi)
	if !ok || !fi.IsDir() || st.Uid != RootUID || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is not a folder only root can write", dir)
	}
	return nil
}

// size is what p takes, without following links, on its own device.
func size(p string) int64 {
	var total int64
	fi0, err := os.Lstat(p)
	if err != nil {
		return 0
	}
	st0, _ := stat(fi0)
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := stat(fi); ok {
			if st0 != nil && st.Dev != st0.Dev {
				return fs.SkipDir
			}
			total += st.Blocks * 512
		}
		return nil
	})
	return total
}

func freeBytes(dir string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, err
	}
	return s.Bavail * uint64(s.Bsize), nil // #nosec G115 -- block size is positive
}

// RunArchive runs op's job: each conffile is copied into its own archive,
// then each leftover is renamed beside itself (only root can write its
// folder), checked to be the file previewed, archived, and removed
// through a directory handle that follows no link. The manifest is
// written after each entry, so a failure leaves a true record. The first
// failure stops the run.
func RunArchive(ctx context.Context, r Roots, op string, tar Tar) error {
	j, err := readJob(r, op)
	if err != nil {
		return err
	}
	dir := r.RemovedOp(op)
	if err := os.MkdirAll(r.removed(), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	m, ok, err := ReadManifest(r, op)
	if err != nil {
		return err
	}
	if !ok {
		m = rules.Manifest{Op: op, UID: j.UID, Item: j.Item, Created: time.Now().UnixMilli()}
	}
	if m.UID != j.UID {
		return errors.New("the operation belongs to another user")
	}
	var need int64
	for _, mv := range j.Moves {
		need += size(r.sys(mv.Path))
	}
	if free, err := freeBytes(dir); err == nil && uint64(need+need/10) > free { // #nosec G115 -- sizes are non-negative
		return fmt.Errorf("not enough space in %s: %d bytes needed, %d free", r.removed(), need+need/10, free)
	}
	for _, c := range j.Conffiles {
		if err := copyConffile(ctx, r, &m, c, tar); err != nil {
			return err
		}
	}
	for _, mv := range j.Moves {
		if err := move(ctx, r, &m, mv, tar); err != nil {
			return err
		}
	}
	return nil
}

func copyConffile(ctx context.Context, r Roots, m *rules.Manifest, c Conffile, tar Tar) error {
	fi, err := os.Lstat(r.sys(c.Path))
	if err != nil || !fi.Mode().IsRegular() {
		return nil // gone already, or not a plain file: nothing to keep
	}
	st, _ := stat(fi)
	i := len(m.Entries)
	if i >= rules.MaxEntries {
		return errors.New("too many archived files")
	}
	a := filepath.Join(r.RemovedOp(m.Op), rules.ArchiveName(i))
	if _, _, err := tar(ctx, "leftovers-tar-create", rules.TarCreateArgs(a, r.sys("/"), c.Path[1:])); err != nil {
		return fmt.Errorf("copying %s: %w", c.Path, err)
	}
	sum, err := fileSHA(a)
	if err != nil {
		return err
	}
	m.Entries = append(m.Entries, rules.Entry{Path: c.Path, Kind: rules.EntryConffile, Package: c.Package, UID: st.Uid, GID: st.Gid,
		Mode: uint32(fi.Mode().Perm()), Size: fi.Size(), Dev: st.Dev, Ino: st.Ino, Member: c.Path[1:], Archive: rules.ArchiveName(i), SHA256: sum})
	return writeManifest(r, *m)
}

func move(ctx context.Context, r Roots, m *rules.Manifest, mv Move, tar Tar) error {
	parent, base := filepath.Split(mv.Path)
	parent = filepath.Clean(parent)
	if err := SafeParent(r.sys(parent)); err != nil {
		return err
	}
	root, err := os.OpenRoot(r.sys(parent))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	i := len(m.Entries)
	if i >= rules.MaxEntries {
		return errors.New("too many archived files")
	}
	member := rules.MovedMember(m.Op, i)
	same := func(name string) (fs.FileInfo, error) {
		fi, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !mv.Same(fi) {
			return nil, fmt.Errorf("%s changed since the preview", mv.Path)
		}
		return fi, nil
	}
	if _, err := same(base); err != nil {
		return err
	}
	if err := root.Rename(base, member); err != nil {
		return fmt.Errorf("moving %s aside: %w", mv.Path, err)
	}
	fi, err := same(member)
	if err != nil {
		_ = root.Rename(member, base)
		return err
	}
	st, _ := stat(fi)
	kind := rules.EntryFile
	switch {
	case fi.IsDir():
		kind = rules.EntryDir
	case fi.Mode()&fs.ModeSymlink != 0:
		kind = rules.EntryLink
	}
	a := filepath.Join(r.RemovedOp(m.Op), rules.ArchiveName(i))
	if _, _, err := tar(ctx, "leftovers-tar-create", rules.TarCreateArgs(a, r.sys(parent), member)); err != nil {
		_ = os.Remove(a)
		_ = root.Rename(member, base)
		return fmt.Errorf("archiving %s: %w", mv.Path, err)
	}
	sum, err := fileSHA(a)
	if err != nil {
		_ = root.Rename(member, base)
		return err
	}
	m.Entries = append(m.Entries, rules.Entry{Path: mv.Path, Kind: kind, UID: st.Uid, GID: st.Gid, Mode: uint32(fi.Mode()),
		Size: size(filepath.Join(r.sys(parent), member)), Dev: st.Dev, Ino: st.Ino, Member: member, Archive: rules.ArchiveName(i), SHA256: sum})
	if err := writeManifest(r, *m); err != nil {
		_ = root.Rename(member, base)
		return err
	}
	if err := root.RemoveAll(member); err != nil {
		return fmt.Errorf("%s is archived but could not be removed (%s): %w", mv.Path, filepath.Join(parent, member), err)
	}
	return nil
}

// WriteRestore writes op's restore job.
func WriteRestore(r Roots, j RestoreJob) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeFile(r.OpDir(j.Op), restoreFile, b)
}

// ReadResults reads what the last restore of op did.
func ReadResults(r Roots, op string) ([]RestoreResult, error) {
	if !rules.ValidOp(op) {
		return nil, errors.New("bad operation ID")
	}
	b, err := readFile(r.OpDir(op), resultFile, maxJob)
	if err != nil {
		return nil, err
	}
	var out []RestoreResult
	return out, decodeStrict(b, &out)
}

// RunRestore puts back the paths of op's restore job, never over anything
// that exists: a moved leftover is extracted beside its place and renamed
// there only while the place is still empty; a configuration file is
// written beside the file the package put back and renamed over it, and
// only while dpkg lists it as a conffile of an installed package. Each
// path's outcome goes into the results; one path failing does not stop
// the others, since nothing is removed.
func RunRestore(ctx context.Context, r Roots, op string, tar Tar) error {
	if !rules.ValidOp(op) {
		return errors.New("bad operation ID")
	}
	b, err := readFile(r.OpDir(op), restoreFile, maxJob)
	if err != nil {
		return err
	}
	var j RestoreJob
	if err := decodeStrict(b, &j); err != nil || j.Op != op || len(j.Paths) == 0 || len(j.Paths) > rules.MaxEntries {
		return errors.New("bad restore job")
	}
	m, ok, err := ReadManifest(r, op)
	if err != nil {
		return fmt.Errorf("no valid manifest: %w", err)
	}
	if !ok {
		return errors.New("no manifest")
	}
	byPath := map[string]int{}
	for i, e := range m.Entries {
		byPath[e.Path] = i
	}
	var conf map[string]string
	var results []RestoreResult
	for _, p := range j.Paths {
		i, ok := byPath[p]
		if !ok {
			results = append(results, RestoreResult{Path: p, Reason: "not in this operation"})
			continue
		}
		e := m.Entries[i]
		var err error
		if e.Kind == rules.EntryConffile {
			if conf == nil {
				conf, _ = InstalledConffiles(r)
			}
			err = restoreConffile(ctx, r, m.Op, i, e, conf, tar)
		} else {
			err = restoreMoved(ctx, r, m.Op, e, tar)
		}
		res := RestoreResult{Path: p, Done: err == nil}
		if err != nil {
			res.Reason = err.Error()
		}
		results = append(results, res)
	}
	out, err := json.Marshal(results)
	if err != nil {
		return err
	}
	return writeFile(r.OpDir(op), resultFile, out)
}

// checkArchive verifies an entry's archive is the one written, and that
// every member tar lists stays inside the entry's member. A listing cut
// short (a very large folder) relies on the checksum and on tar
// extracting only the one member it is given.
func checkArchive(ctx context.Context, r Roots, op string, e rules.Entry, tar Tar) (string, error) {
	a := filepath.Join(r.RemovedOp(op), e.Archive)
	fi, err := os.Lstat(a)
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("its archive is missing")
	}
	sum, err := fileSHA(a)
	if err != nil || sum != e.SHA256 {
		return "", errors.New("its archive does not match the manifest")
	}
	out, truncated, err := tar(ctx, "leftovers-tar-list", rules.TarListArgs(a))
	if err != nil {
		return "", fmt.Errorf("its archive could not be read: %w", err)
	}
	if !truncated {
		for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if err := rules.CheckMember(e, l); err != nil {
				return "", err
			}
		}
	}
	return a, nil
}

func restoreMoved(ctx context.Context, r Roots, op string, e rules.Entry, tar Tar) error {
	parent, base := filepath.Split(e.Path)
	parent = filepath.Clean(parent)
	if err := SafeParent(r.sys(parent)); err != nil {
		return err
	}
	if _, err := os.Lstat(r.sys(e.Path)); err == nil {
		return errors.New("something is there again; it was left alone")
	}
	if _, err := os.Lstat(filepath.Join(r.sys(parent), e.Member)); err == nil {
		return fmt.Errorf("%s is in the way", filepath.Join(parent, e.Member))
	}
	a, err := checkArchive(ctx, r, op, e, tar)
	if err != nil {
		return err
	}
	if _, _, err := tar(ctx, "leftovers-tar-extract", rules.TarExtractArgs(a, r.sys(parent), e.Member)); err != nil {
		return fmt.Errorf("extracting: %w", err)
	}
	if err := renameNoReplace(r.sys(parent), e.Member, base); err != nil {
		return fmt.Errorf("extracted as %s, but not put in place: %w", filepath.Join(parent, e.Member), err)
	}
	return nil
}

func restoreConffile(ctx context.Context, r Roots, op string, i int, e rules.Entry, conf map[string]string, tar Tar) error {
	if pkg, ok := conf[e.Path]; !ok {
		return errors.New("no installed package lists it as a configuration file; reinstall the package first")
	} else if n, _, _ := strings.Cut(e.Package, ":"); n != pkg {
		return fmt.Errorf("it now belongs to %s", pkg)
	}
	dir := filepath.Dir(r.sys(e.Path))
	if err := SafeParent(dir); err != nil {
		return err
	}
	if fi, err := os.Lstat(r.sys(e.Path)); err == nil && !fi.Mode().IsRegular() {
		return errors.New("something other than a file is there; it was left alone")
	}
	a, err := checkArchive(ctx, r, op, e, tar)
	if err != nil {
		return err
	}
	data, truncated, err := tar(ctx, "leftovers-tar-cat", rules.TarCatArgs(a, e.Member))
	if err != nil || truncated || int64(len(data)) != e.Size || len(data) > maxConf {
		return errors.New("its archive could not be read in full")
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".hearth-restore-%s-%d", op, i))
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed name in a root-only folder
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cherr := f.Chown(int(e.UID), int(e.GID))
	mderr := f.Chmod(fs.FileMode(e.Mode).Perm())
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, cherr, mderr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, r.sys(e.Path)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ListManifests are the valid manifests of uid's operations (all of them
// for root), newest first.
func ListManifests(r Roots, uid uint32) []rules.Manifest {
	ents, err := os.ReadDir(r.removed())
	if err != nil {
		return nil
	}
	var out []rules.Manifest
	for _, e := range ents {
		if !e.IsDir() || !rules.ValidOp(e.Name()) {
			continue
		}
		m, ok, err := ReadManifest(r, e.Name())
		if err != nil || !ok || (uid != 0 && m.UID != uid) {
			continue
		}
		out = append(out, m)
	}
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Created > out[k-1].Created; k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

// Clean removes archives older than Keep and plans older than a day.
// Only folders named as operations are touched.
func Clean(r Roots, now time.Time) error {
	var errs []error
	for _, c := range []struct {
		dir  string
		keep time.Duration
	}{{r.removed(), Keep}, {r.uninstall(), keepPlan}} {
		ents, err := os.ReadDir(c.dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || !rules.ValidOp(e.Name()) {
				continue
			}
			created := time.Time{}
			if c.dir == r.removed() {
				if m, ok, err := ReadManifest(r, e.Name()); err == nil && ok {
					created = time.UnixMilli(m.Created)
				}
			}
			if created.IsZero() {
				if fi, err := e.Info(); err == nil {
					created = fi.ModTime()
				}
			}
			if now.Sub(created) < c.keep {
				continue
			}
			root, err := os.OpenRoot(c.dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			errs = append(errs, root.RemoveAll(e.Name()))
			_ = root.Close()
		}
	}
	return errors.Join(errs...)
}
