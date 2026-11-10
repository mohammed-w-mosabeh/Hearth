package uninstall

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// compositors are the programs that draw a graphical session; the package
// owning the one running is a desktop package (docs/plan/uninstall.md
// §2.1).
var compositors = map[string]bool{
	"gnome-shell": true, "kwin_wayland": true, "kwin_x11": true, "mutter": true, "xfwm4": true, "marco": true,
	"muffin": true, "cinnamon": true, "sway": true, "Hyprland": true, "weston": true, "labwc": true,
	"wayfire": true, "river": true, "niri": true, "openbox": true, "Xwayland": true, "Xorg": true,
}

// maxProcScan bounds the processes looked at.
const maxProcScan = 8192

// SessionPrograms lists the programs the current graphical session runs
// on: uid's compositor processes (their exe, read from proc, a
// /proc-like folder) and the display manager's unit file, which
// /etc/systemd/system/display-manager.service links to (dmLink). Owners
// of these paths are desktop packages. Nothing found is not an error.
func SessionPrograms(proc string, uid int, dmLink string) []string {
	var out []string
	if t, err := os.Readlink(dmLink); err == nil {
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(dmLink), t)
		}
		out = append(out, filepath.Clean(t))
	}
	ents, err := os.ReadDir(proc)
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for i, e := range ents {
		if i >= maxProcScan {
			break
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join(proc, e.Name())
		fi, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(dir, "comm")) // #nosec G304 -- a /proc entry
		if err != nil || !compositors[strings.TrimSpace(string(comm))] {
			continue
		}
		exe, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil || !filepath.IsAbs(exe) || seen[exe] {
			continue
		}
		seen[exe] = true
		out = append(out, strings.TrimSuffix(exe, " (deleted)"))
	}
	return out
}
