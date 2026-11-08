package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Tar runs tar with args (safeexec in production) and returns its exit
// code.
type Tar func(ctx context.Context, args []string) (int, error)

// Roots are where the units find things; tests replace them.
type Roots struct {
	Backups string // default Dir
	Volumes string // default VolumesDir
}

func (r Roots) backups() string {
	if r.Backups == "" {
		return Dir
	}
	return r.Backups
}

func (r Roots) volumes() string {
	if r.Volumes == "" {
		return VolumesDir
	}
	return r.Volumes
}

// ChangedName marks a backup whose files changed while tar read them; the
// broker records it as the manifest's warning.
func ChangedName(output string) string { return strings.TrimSuffix(output, ".partial") + ".changed" }

// readPlan reads a plan file from the stack's backup folder, through
// os.Root so no link leads out of it, and decodes it strictly.
func readPlan(roots Roots, stack, file string, v any) error {
	r, err := os.OpenRoot(roots.backups() + "/" + stack)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	f, err := r.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return errors.New("the plan is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return errors.New("the plan cannot be read")
	}
	return nil
}

// RunBackup is hearth-backup@<stack>: it reads the broker's plan and runs
// tar. Exit 1 from tar means a file changed while it was read: the archive
// is kept and marked.
func RunBackup(ctx context.Context, roots Roots, stack string, tar Tar) error {
	var p Plan
	if err := readPlan(roots, stack, PlanFile, &p); err != nil {
		return err
	}
	if p.Stack != stack {
		return errors.New("the plan is another stack's")
	}
	if err := p.Check(); err != nil {
		return err
	}
	code, err := tar(ctx, TarCreate(p))
	switch {
	case code == 1:
		r, err := os.OpenRoot(roots.backups() + "/" + stack)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		f, err := r.OpenFile(ChangedName(p.Output), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	case err != nil:
		return fmt.Errorf("tar failed with %d", code)
	}
	return nil
}

// FileSHA256 hashes a file of the stack's backup folder.
func FileSHA256(roots Roots, stack, name string) (string, int64, error) {
	r, err := os.OpenRoot(roots.backups() + "/" + stack)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = r.Close() }()
	f, err := r.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, errors.New("not a file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// RunRestore is hearth-restore@<stack>: it checks the archive is the one
// the manifest hashed, empties each volume's data, then extracts those
// volumes and secrets.env. The unit can write nothing else.
func RunRestore(ctx context.Context, roots Roots, stack string, tar Tar) error {
	var p RestorePlan
	if err := readPlan(roots, stack, RestoreFile, &p); err != nil {
		return err
	}
	if p.Stack != stack {
		return errors.New("the plan is another stack's")
	}
	if err := p.Check(); err != nil {
		return err
	}
	sum, _, err := FileSHA256(roots, stack, p.Archive)
	if err != nil {
		return err
	}
	if sum != p.SHA256 {
		return errors.New("the archive is not the one its manifest hashed")
	}
	for _, v := range p.Volumes {
		if err := Empty(roots, v); err != nil {
			return fmt.Errorf("emptying %s: %w", v, err)
		}
	}
	if code, err := tar(ctx, TarExtract(p)); err != nil {
		return fmt.Errorf("tar failed with %d", code)
	}
	return nil
}

// Empty removes everything inside a volume's _data, never following a
// link: _data itself must be a folder, not a link, and stays.
func Empty(roots Roots, volume string) error {
	if !ValidVolume(volume) {
		return errors.New("not a volume name")
	}
	top, err := os.OpenRoot(roots.volumes())
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	for _, p := range []string{volume, volume + "/_data"} {
		fi, err := top.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return errors.New("the volume's data is not a folder")
		}
	}
	data, err := top.OpenRoot(volume + "/_data")
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	d, err := data.Open(".")
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := data.RemoveAll(e.Name()); err != nil {
			return err
		}
	}
	return nil
}
