package posture

import (
	"os"
	"path/filepath"
	"syscall"
)

// Limits on the PATH scan.
const (
	maxPathEntries  = 20000 // per directory
	maxPathFindings = 100
)

// Writable is a directory in PATH, or a program in one, that a user
// other than its owner can change, so a program could be swapped for
// another.
type Writable struct {
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Mode  string `json:"mode"`           // e.g. "drwxrwxrwx"
	Owner uint32 `json:"owner"`          // uid
	Why   string `json:"why"`            // "world" (anyone may write), "owner" (another user owns it) or "relative" (a relative PATH entry)
	Link  string `json:"link,omitempty"` // for a program, the file a symlink names
}

// scanPath checks each directory of dirs (and what it holds) for write
// access by anyone, or ownership by a user who is neither root nor uid.
func scanPath(dirs []string, uid uint32) (out []Writable, truncated bool) {
	seen := map[string]bool{}
	add := func(w Writable) {
		if len(out) >= maxPathFindings {
			truncated = true
			return
		}
		out = append(out, w)
	}
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			// The current directory, whatever it is: anything there runs.
			if d == "" {
				d = "."
			}
			if !seen[d] {
				seen[d] = true
				add(Writable{Path: d, Dir: true, Why: "relative"})
			}
			continue
		}
		d = filepath.Clean(d)
		if seen[d] {
			continue
		}
		seen[d] = true
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		if why := badMode(fi, uid); why != "" {
			add(Writable{Path: d, Dir: true, Mode: fi.Mode().String(), Owner: owner(fi), Why: why})
		}
		f, err := os.Open(d) // #nosec G304 -- a PATH directory, only listed
		if err != nil {
			continue
		}
		entries, _ := f.ReadDir(maxPathEntries)
		_ = f.Close()
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			fi, err := os.Stat(p) // follows a symlink to the program it runs
			if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
				continue
			}
			if why := badMode(fi, uid); why != "" {
				w := Writable{Path: p, Mode: fi.Mode().String(), Owner: owner(fi), Why: why}
				if e.Type()&os.ModeSymlink != 0 {
					w.Link, _ = filepath.EvalSymlinks(p)
				}
				add(w)
			}
		}
	}
	return out, truncated
}

// badMode says why fi is a risk in PATH, or "".
func badMode(fi os.FileInfo, uid uint32) string {
	if fi.Mode().Perm()&0o002 != 0 {
		return "world"
	}
	if o := owner(fi); o != 0 && o != uid {
		return "owner"
	}
	return ""
}

func owner(fi os.FileInfo) uint32 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	return 0
}
