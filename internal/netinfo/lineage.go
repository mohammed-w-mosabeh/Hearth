package netinfo

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Lineage is what is behind a socket's owner (SPEC 11 "What is this?"):
// its unit, its program and the packages they come from. The container
// (from the owner's cgroup or a published port) is matched by the UI.
type Lineage struct {
	Unit *UnitInfo `json:"unit,omitempty"`
	// Exe is the program: the process's own when it is the user's
	// (ExeExact), else the unit's ExecStart.
	Exe         string  `json:"exe,omitempty"`
	ExeExact    bool    `json:"exe_exact,omitempty"`
	ExePackage  *PkgRef `json:"exe_package,omitempty"`
	UnitPackage *PkgRef `json:"unit_package,omitempty"`
}

// UnitInfo is a unit as systemd describes it.
type UnitInfo struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"` // system or user
	Description string `json:"description,omitempty"`
	Active      string `json:"active,omitempty"`
	Sub         string `json:"sub,omitempty"`
	File        string `json:"file,omitempty"` // FragmentPath; "" for transient units
	MainPID     uint32 `json:"main_pid,omitempty"`
}

// PkgRef is a Debian package.
type PkgRef struct {
	Name    string `json:"name"`
	ID      string `json:"id"` // name:arch
	Version string `json:"version,omitempty"`
}

// Packages finds the dpkg owners of files (*discovery.Dpkg).
type Packages interface {
	Owners(paths []string) (map[string]string, error)
	ByName(name string) ([]discovery.Package, error)
}

const (
	sdDest = "org.freedesktop.systemd1"
	sdPath = dbus.ObjectPath("/org/freedesktop/systemd1")
)

// HandleLineage serves GET /api/v1/net/lineage?scope=system|user&unit=<name>&pid=<pid>&start=<start>.
// unit, or pid with start, or both.
func (c Config) HandleLineage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, unit := q.Get("scope"), q.Get("unit")
	pid, perr := strconv.Atoi(q.Get("pid"))
	start, serr := strconv.ParseUint(q.Get("start"), 10, 64)
	hasPID := q.Get("pid") != ""
	switch {
	case unit != "" && (safeexec.UnitName(unit) != nil || (scope != "system" && scope != "user")):
		http.Error(w, "unit must be a unit name with scope system or user", http.StatusBadRequest)
		return
	case hasPID && (perr != nil || serr != nil || pid <= 0):
		http.Error(w, "pid and start must be numbers", http.StatusBadRequest)
		return
	case unit == "" && !hasPID:
		http.Error(w, "unit or pid required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out Lineage
	var execStart string
	if unit != "" {
		out.Unit, execStart = c.unitInfo(ctx, scope, unit)
	}
	if hasPID {
		if exe := c.Resolver.exe(pid, start); exe != "" {
			out.Exe, out.ExeExact = exe, true
		}
	}
	if out.Exe == "" {
		out.Exe = execStart
	}
	if c.Packages != nil {
		var paths []string
		if out.Unit != nil && out.Unit.File != "" {
			paths = append(paths, out.Unit.File)
		}
		if out.Exe != "" {
			paths = append(paths, out.Exe)
		}
		owners, err := c.Packages.Owners(paths)
		if err != nil {
			c.Log.Debug("net: package owners", "err", err)
		}
		if out.Unit != nil && out.Unit.File != "" {
			out.UnitPackage = c.pkgRef(owners[out.Unit.File])
		}
		if out.Exe != "" {
			out.ExePackage = c.pkgRef(owners[out.Exe])
		}
	}
	writeJSON(w, out)
}

// unitInfo reads a loaded unit's properties and its ExecStart program.
func (c Config) unitInfo(ctx context.Context, scope, name string) (*UnitInfo, string) {
	dial := c.Bus
	if scope == "user" {
		dial = c.Session
	}
	if dial == nil {
		return nil, ""
	}
	bus, err := dial(ctx)
	if err != nil {
		c.Log.Debug("net: lineage bus", "scope", scope, "err", err)
		return nil, ""
	}
	if cl, ok := bus.(io.Closer); ok {
		defer func() { _ = cl.Close() }()
	}
	var path dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, "org.freedesktop.systemd1.Manager.GetUnit", []any{&path}, name); err != nil {
		return nil, "" // not loaded
	}
	props, err := bus.GetAll(ctx, sdDest, path, "org.freedesktop.systemd1.Unit")
	if err != nil {
		return nil, ""
	}
	u := &UnitInfo{Name: name, Scope: scope, Description: str(props["Description"], 300),
		Active: str(props["ActiveState"], 32), Sub: str(props["SubState"], 32)}
	if f := str(props["FragmentPath"], 4096); filepath.IsAbs(f) {
		u.File = filepath.Clean(f)
	}
	if !strings.HasSuffix(name, ".service") {
		return u, ""
	}
	sp, err := bus.GetAll(ctx, sdDest, path, "org.freedesktop.systemd1.Service")
	if err != nil {
		return u, ""
	}
	u.MainPID, _ = sp["MainPID"].Value().(uint32)
	return u, execPath(sp["ExecStart"])
}

// execPath is the program of ExecStart's first command, a(sasbttttuii).
func execPath(v dbus.Variant) string {
	cmds, ok := v.Value().([][]any)
	if !ok || len(cmds) == 0 || len(cmds[0]) == 0 {
		return ""
	}
	p, _ := cmds[0][0].(string)
	if !filepath.IsAbs(p) || len(p) > 4096 {
		return ""
	}
	return filepath.Clean(p)
}

// pkgRef describes the package owning a file ("name" or "name:arch").
func (c Config) pkgRef(owner string) *PkgRef {
	if owner == "" {
		return nil
	}
	name, arch, _ := strings.Cut(owner, ":")
	ref := &PkgRef{Name: name, ID: owner}
	pkgs, err := c.Packages.ByName(name)
	if err != nil {
		return ref
	}
	for _, p := range pkgs {
		if arch == "" || p.Arch == arch {
			ref.ID, ref.Version = p.ID(), p.Version
			break
		}
	}
	return ref
}

// exe is the program of process pid if it is still the process started
// at start and its exe link is readable (the user's own processes).
func (r *Resolver) exe(pid int, start uint64) string {
	if p := r.proc(pid); p.start == 0 || p.start != start {
		return ""
	}
	link, err := os.Readlink(filepath.Join(r.procRoot(), strconv.Itoa(pid), "exe"))
	if err != nil || !filepath.IsAbs(link) {
		return ""
	}
	return strings.TrimSuffix(link, " (deleted)")
}
