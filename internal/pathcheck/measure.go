package pathcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// The walk's bounds: the unit stops after TimeoutStartSec=30s, and the
// broker's client waits 25 s for a Measure answer.
const (
	walkBudget  = 15 * time.Second
	walkEntries = 2_000_000
)

// CheckMeasure is Check, then the size of each bind's real path and the
// device of the backups' filesystem (ROADMAP 9.10). Sizing reads
// directories and file metadata only, never a file's content.
func (r Resolver) CheckMeasure(raw []byte) Reply {
	out := r.Check(raw)
	if !out.OK {
		return out
	}
	mi, err := r.Mounts()
	if err != nil {
		return refused("mountinfo: " + err.Error())
	}
	real, id, err := r.Open(BackupsParent)
	if err != nil {
		return refused("the backups' folder: " + openErr(err))
	}
	dev, _, ok := locate(parseMountinfo(mi), id, real)
	if !ok {
		return refused("the backups' folder: its mount was not found")
	}
	out.BackupDev = dev
	out.Sizes = map[string]Size{}
	for _, b := range out.Binds {
		if _, done := out.Sizes[b.Real]; done || r.Measure == nil {
			continue
		}
		out.Sizes[b.Real] = r.Measure(b.Real)
	}
	return out
}

// Walk adds up the sizes of the regular files under root, staying on
// root's filesystem and following no link, for at most budget and
// entries; past either the result is Partial.
func Walk(root string, budget time.Duration, entries int) Size {
	var s Size
	fi, err := os.Lstat(root)
	if err != nil {
		return Size{Partial: true}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Size{Partial: true}
	}
	if !fi.IsDir() {
		if fi.Mode().IsRegular() {
			s.Bytes = fi.Size()
		}
		return s
	}
	dev := st.Dev
	deadline := time.Now().Add(budget)
	n := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		n++
		if n > entries || (n%1024 == 0 && time.Now().After(deadline)) {
			s.Partial = true
			return fs.SkipAll
		}
		if err != nil {
			s.Partial = true // unreadable: what it holds is not counted
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // removed in between
		}
		if d.IsDir() {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Dev != dev {
				return fs.SkipDir // another filesystem: tar skips it too
			}
			return nil
		}
		if info.Mode().IsRegular() {
			s.Bytes += info.Size()
		}
		return nil
	})
	return s
}
