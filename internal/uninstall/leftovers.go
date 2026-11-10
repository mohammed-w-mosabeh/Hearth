package uninstall

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// User leftovers (docs/plan/uninstall.md §2.4): direct children of the
// user roots that match the item, never shared folders, never a path
// another package owns, never a name that also belongs to an app that
// stays.

// Scan finds leftovers.
type Scan struct {
	Home string
	UID  uint32
	// Owned maps paths to the dpkg package owning them (discovery.Dpkg.Owners).
	Owned func(paths []string) (map[string]string, error)
	// Others are the names of everything that stays installed: the other
	// apps' desktop IDs, app IDs, window classes and programs, the other
	// packages and snaps.
	Others Identifiers
}

// Bounds of a scan.
const (
	maxRootEntries = 5000
	largeBytes     = 1 << 30
	measureEntries = 200000
	measureTime    = 3 * time.Second
)

// UserLeftovers lists the leftovers of ids under the home: matched ones as
// candidates, and those left alone with their reason. Paths already in
// the plan (skip) are not listed twice.
func (s Scan) UserLeftovers(ctx context.Context, ids Identifiers, skip map[string]bool) []Candidate {
	others := s.Others.Clean()
	var out []Candidate
	var paths []string
	found := map[string]Match{}
	for _, root := range append([]string{""}, UserRoots...) {
		dir := filepath.Join(s.Home, root)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for i, e := range ents {
			if i >= maxRootEntries {
				break
			}
			p := filepath.Join(dir, e.Name())
			if skip[p] {
				continue
			}
			r, name, err := CheckUser(s.Home, p)
			if err != nil || r != root {
				continue
			}
			m, ok := ids.MatchUser(r, name)
			if !ok {
				continue
			}
			if why := othersHave(others, m, name); why != "" {
				out = append(out, remains(p, p, why))
				continue
			}
			found[p] = m
			paths = append(paths, p)
		}
	}
	owned := map[string]string{}
	if s.Owned != nil && len(paths) > 0 {
		if o, err := s.Owned(paths); err == nil {
			owned = o
		}
	}
	for _, p := range paths {
		m := found[p]
		if pkg := owned[p]; pkg != "" {
			out = append(out, remains(p, p, "belongs to the installed package "+pkg))
			continue
		}
		uid, _, err := owner(p)
		if err != nil {
			continue
		}
		if uid != s.UID {
			out = append(out, remains(p, p, "not yours"))
			continue
		}
		c, ok := pathCandidate(GroupUser, KindTrash, p, m.Reason, m.Preselect)
		if !ok {
			continue
		}
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			c.Reason += "; a link, so only the link goes"
		} else if fi != nil && fi.IsDir() {
			c.Size = measure(ctx, p)
		}
		if c.Size >= largeBytes {
			c.Warn = "large"
		}
		out = append(out, c)
	}
	return out
}

// othersHave says why a match must be left alone because something that
// stays installed has the same name, or "".
func othersHave(others Identifiers, m Match, name string) string {
	if m.Preselect {
		if slices.Contains(others.AppIDs, m.Value) || slices.Contains(others.Snaps, m.Value) {
			return fmt.Sprintf("%s also belongs to an app that stays installed", m.Value)
		}
		return ""
	}
	f := fold(name)
	for _, vs := range [][]string{others.Packages, others.Exec, others.Desktop, others.WMClass, others.AppIDs, others.Snaps, others.Names} {
		for _, v := range vs {
			if fold(v) == f {
				return fmt.Sprintf("also named like %s, which stays installed", v)
			}
		}
	}
	return ""
}

// measure is the space a folder takes on its own device, bounded in
// entries and time; links are not followed.
func measure(ctx context.Context, dir string) int64 {
	st0, err := os.Lstat(dir)
	if err != nil {
		return 0
	}
	sys0, _ := st0.Sys().(*syscall.Stat_t)
	ctx, cancel := context.WithTimeout(ctx, measureTime)
	defer cancel()
	var total int64
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		n++
		if err != nil || n > measureEntries || ctx.Err() != nil {
			return fs.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if sys0 != nil && st.Dev != sys0.Dev {
				return fs.SkipDir
			}
			total += st.Blocks * 512
		}
		return nil
	})
	return total
}
