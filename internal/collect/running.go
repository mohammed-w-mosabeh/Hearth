package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RunningApps returns the IDs (as in "app:<id>" groups) of the apps with
// processes in the user's app.slice. It reads only cgroup directories and
// cgroup.events files, so it is cheap enough to poll from the UI.
func RunningApps(cgroupRoot string, uid int) []string {
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}
	u := strconv.Itoa(uid)
	rel := "/user.slice/user-" + u + ".slice/user@" + u + ".service/app.slice"
	seen := map[string]bool{}
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(filepath.Join(cgroupRoot, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			child := rel + "/" + name
			switch {
			case strings.HasSuffix(name, ".slice") && depth < 3:
				walk(child, depth+1) // e.g. app-gnome.slice
			case strings.HasSuffix(name, ".scope") || strings.HasSuffix(name, ".service"):
				g := groupFor(child, "")
				if g.kind == KindApp && populated(filepath.Join(cgroupRoot, child)) {
					seen[strings.TrimPrefix(g.id, "app:")] = true
				}
			}
		}
	}
	walk(rel, 0)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// populated reports whether the cgroup (or one below it) has processes.
func populated(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.events")) // #nosec G304 -- under the cgroup root
	return err == nil && bytes.Contains(b, []byte("populated 1"))
}
