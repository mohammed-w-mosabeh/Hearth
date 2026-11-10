package uninstall

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The trash step (SECURITY S16c, S16k, S17): user files go to the XDG
// trash through gio, as a transient unit of the user's manager, at most 16
// a call. Each path is checked again by device and inode just before, so a
// path swapped since the preview is refused, and what went where is read
// back from the trash's own info files for undo.

// RunFunc runs one fixed command as the user (pkgs.UserRunner.Run).
type RunFunc func(ctx context.Context, desc, path string, argv, env []string) (string, error)

// Trasher moves user files to the trash.
type Trasher struct {
	Home string
	Gio  string // default /usr/bin/gio
	Run  RunFunc
}

// Trashed is one path in the trash: Name is its file in Trash/files, URI
// what `gio trash --restore` takes. URI is "" when the path went to a
// trash on another disk, which only the user can restore from.
type Trashed struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
	URI  string `json:"uri,omitempty"`
}

// ErrChanged is a path that is no longer the one previewed.
var ErrChanged = errors.New("changed since the preview")

// maxTrashBatch is the paths one gio call takes (S16c).
const maxTrashBatch = 16

// Recheck refuses c unless its path is still the file previewed: same
// device and inode, so a link or another file put in its place is refused.
func Recheck(c Candidate) error {
	fi, err := os.Lstat(c.ID)
	if err != nil {
		return fmt.Errorf("%s: %w", c.ID, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Dev != c.Dev || st.Ino != c.Ino || fi.Mode().Type() != c.Type {
		return fmt.Errorf("%s: %w", c.ID, ErrChanged)
	}
	return nil
}

// infoDir is the home trash's info folder.
func (t Trasher) infoDir() string { return filepath.Join(t.Home, ".local/share/Trash/info") }

// infoNames lists the info files now in the home trash.
func (t Trasher) infoNames() map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(t.infoDir())
	if err != nil {
		return out
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".trashinfo") {
			out[e.Name()] = true
		}
	}
	return out
}

// infoPath reads the original path of an info file.
func infoPath(p string) (string, bool) {
	b, ok := readSmall(p)
	if !ok {
		return "", false
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "Path="); ok {
			orig, err := url.PathUnescape(v)
			return orig, err == nil && filepath.IsAbs(orig)
		}
	}
	return "", false
}

// Trash moves cs to the trash in order, rechecking each batch first. It
// stops at the first problem and returns what is in the trash so far with
// the error; nothing after the failure is touched.
func (t Trasher) Trash(ctx context.Context, cs []Candidate) ([]Trashed, error) {
	gio := t.Gio
	if gio == "" {
		gio = "/usr/bin/gio"
	}
	var done []Trashed
	for len(cs) > 0 {
		n := min(len(cs), maxTrashBatch)
		batch := cs[:n]
		cs = cs[n:]
		argv := []string{"gio", "trash", "--"}
		for _, c := range batch {
			if c.Kind != KindTrash {
				return done, fmt.Errorf("%s is not for the trash", c.ID)
			}
			if err := Recheck(c); err != nil {
				return done, err
			}
			argv = append(argv, c.ID)
		}
		before := t.infoNames()
		_, runErr := t.Run(ctx, "Move leftovers to the trash", gio, argv, nil)
		got := map[string]Trashed{}
		for name := range t.infoNames() {
			if before[name] {
				continue
			}
			orig, ok := infoPath(filepath.Join(t.infoDir(), name))
			if !ok {
				continue
			}
			file := strings.TrimSuffix(name, ".trashinfo")
			got[orig] = Trashed{Path: orig, Name: file, URI: "trash:///" + url.PathEscape(file)}
		}
		// Record everything that did go, even when gio stopped partway.
		for _, c := range batch {
			if _, err := os.Lstat(c.ID); err == nil {
				if runErr == nil {
					runErr = fmt.Errorf("%s is still there", c.ID)
				}
				continue
			}
			tr, ok := got[c.ID]
			if !ok {
				tr = Trashed{Path: c.ID} // another disk's trash
			}
			done = append(done, tr)
		}
		if runErr != nil {
			return done, runErr
		}
	}
	return done, nil
}
