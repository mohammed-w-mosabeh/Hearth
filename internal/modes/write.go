package modes

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// deletedDir holds deleted mode files, so a deletion can be undone by hand
// (SECURITY S17). The loader skips it: its name starts with a dot.
const deletedDir = ".deleted"

// ErrChanged means a mode file changed on disk since it was read.
var ErrChanged = errors.New("the mode file changed on disk; reload and try again")

// Encode renders a mode as its file. Comments of a hand-written file are
// not kept.
func Encode(m Mode) ([]byte, error) {
	f := file{Name: m.Name, Session: m.Session, Stacks: m.Stacks, Items: m.Items, Trigger: m.Triggers}
	var b bytes.Buffer
	b.WriteString("# A Hearth mode. Hearth rewrites this file when the mode is edited in the app.\n")
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return nil, fmt.Errorf("modes: encode: %w", err)
	}
	return b.Bytes(), nil
}

// Slug turns a name into a mode ID: lowercase letters and digits, the rest
// as single dashes, at most 40 characters; "mode" when nothing is left.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 34 { // room for a "-NN" suffix
		s = strings.TrimRight(s[:34], "-")
	}
	if s == "" {
		s = "mode"
	}
	return s
}

// freeID is the first of base, base-2, base-3… for which taken is false.
func freeID(base string, taken func(id string) bool) (string, error) {
	for i := 1; i < 1000; i++ {
		id := base
		if i > 1 {
			id = base + "-" + strconv.Itoa(i)
		}
		if !taken(id) {
			return id, nil
		}
	}
	return "", errors.New("modes: no free file name")
}

// openDir opens the modes folder, creating it.
func openDir(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("modes: %w", err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("modes: %w", err)
	}
	return r, nil
}

// exists reports whether the folder has an entry id.toml of any kind.
func exists(root *os.Root, id string) bool {
	_, err := root.Lstat(id + ".toml")
	return !errors.Is(err, fs.ErrNotExist)
}

// writeFile replaces id.toml with raw atomically, mode 0600. When wantHash
// is not empty, the file on disk must still have that hash; when it is
// empty, the file must not exist yet.
func writeFile(root *os.Root, id string, raw []byte, wantHash string) error {
	name := id + ".toml"
	cur, err := readFile(root, name)
	switch {
	case wantHash == "" && exists(root, id):
		return ErrChanged
	case wantHash != "" && (err != nil || Hash(cur) != wantHash):
		return ErrChanged
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("modes: %w", err)
	}
	tmp := "." + id + ".toml." + hex.EncodeToString(rnd[:])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("modes: %w", err)
	}
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, name)
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("modes: write %s: %w", name, werr)
	}
	return nil
}

// trashFile moves id.toml into the deleted folder, if it still has hash.
func trashFile(root *os.Root, id, hash string, now time.Time) error {
	name := id + ".toml"
	cur, err := readFile(root, name)
	if err != nil || Hash(cur) != hash {
		return ErrChanged
	}
	if err := root.Mkdir(deletedDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("modes: %w", err)
	}
	if fi, err := root.Lstat(deletedDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("modes: %s is not a folder", deletedDir)
	}
	dst := deletedDir + "/" + id + "-" + now.UTC().Format("20060102-150405.000") + ".toml"
	if err := root.Rename(name, dst); err != nil {
		return fmt.Errorf("modes: delete %s: %w", name, err)
	}
	return nil
}
