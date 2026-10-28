package discovery

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// canonical is p with its directory's symlinks resolved (/bin/ls is
// /usr/bin/ls on a merged-/usr system), the file itself left alone.
func canonical(p string, cache map[string]string) string {
	dir, base := path.Split(p)
	c, ok := cache[dir]
	if !ok {
		c = dir
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			c = r + "/"
		}
		cache[dir] = c
	}
	return c + base
}

// Owners maps each of paths to the dpkg package ("name" or "name:arch",
// as in info/<package>.list) whose file list has it. Directories' symlinks
// are resolved on both sides (/bin/ls is /usr/bin/ls on a merged-/usr
// system); the file itself is not, so a link is owned by whoever shipped
// the link. Paths no package lists are left out.
func (d *Dpkg) Owners(paths []string) (map[string]string, error) {
	cache := map[string]string{}
	want := map[string][]string{} // canonical -> the paths asked
	bases := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		c := canonical(p, cache)
		want[c] = append(want[c], p)
		bases[path.Base(p)] = true
	}
	out := map[string]string{}
	if len(want) == 0 {
		return out, nil
	}
	info := filepath.Join(filepath.Dir(d.cfg.Status), "info")
	lists, err := os.ReadDir(info)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		pkg, ok := strings.CutSuffix(l.Name(), ".list")
		if !ok {
			continue
		}
		f, err := os.Open(filepath.Join(info, l.Name())) // #nosec G304 -- dpkg's own info directory
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			line := sc.Text()
			if !bases[path.Base(line)] {
				continue
			}
			for _, p := range want[canonical(line, cache)] {
				if _, done := out[p]; !done {
					out[p] = pkg
				}
			}
		}
		_ = f.Close()
	}
	return out, nil
}
