package archive

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Package is what root needs to know of one package in dpkg's status.
type Package struct {
	Name, Arch, Status   string // Status: "install ok installed", "deinstall ok config-files", ...
	Essential, Protected bool
	Priority             string
	Conffiles            []string
}

// ID is "name:arch".
func (p Package) ID() string { return p.Name + ":" + p.Arch }

// ConfigOnly reports whether only its configuration files are left.
func (p Package) ConfigOnly() bool { return strings.HasSuffix(p.Status, " config-files") }

// Installed reports whether it is installed.
func (p Package) Installed() bool { return strings.HasSuffix(p.Status, " installed") }

// Undeletable is why it must never be removed or purged (SECURITY S16).
func (p Package) Undeletable() string {
	switch {
	case p.Name == "hearth":
		return "self"
	case p.Essential:
		return "essential"
	case p.Protected:
		return "protected"
	case p.Priority == "required":
		return "required"
	}
	return ""
}

// maxStatus bounds dpkg's status file.
const maxStatus = 64 << 20

// ReadStatus reads every package in dpkg's status file.
func ReadStatus(r Roots) ([]Package, error) {
	f, err := os.Open(r.status())
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, maxStatus))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var out []Package
	var p Package
	inConf := false
	flush := func() {
		if p.Name != "" {
			out = append(out, p)
		}
		p, inConf = Package{}, false
	}
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			flush()
			continue
		}
		if strings.HasPrefix(l, " ") {
			if inConf {
				// " /etc/foo.conf <md5> [obsolete|remove-on-upgrade]"
				if f := strings.Fields(l); len(f) >= 2 && strings.HasPrefix(f[0], "/") && (len(f) == 2 || f[2] != "obsolete") {
					p.Conffiles = append(p.Conffiles, f[0])
				}
			}
			continue
		}
		inConf = false
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Package":
			p.Name = v
		case "Architecture":
			p.Arch = v
		case "Status":
			p.Status = v
		case "Essential":
			p.Essential = v == "yes"
		case "Protected":
			p.Protected = v == "yes"
		case "Priority":
			p.Priority = v
		case "Conffiles":
			inConf = true
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("dpkg's status is empty")
	}
	return out, nil
}

// Find returns the packages matching id ("name" or "name:arch").
func Find(pkgs []Package, id string) []Package {
	name, arch, _ := strings.Cut(id, ":")
	var out []Package
	for _, p := range pkgs {
		if p.Name == name && (arch == "" || p.Arch == arch) {
			out = append(out, p)
		}
	}
	return out
}

// InstalledConffiles maps each conffile of an installed package to its
// package name.
func InstalledConffiles(r Roots) (map[string]string, error) {
	pkgs, err := ReadStatus(r)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range pkgs {
		if p.Installed() {
			for _, c := range p.Conffiles {
				out[c] = p.Name
			}
		}
	}
	return out, nil
}

// renameNoReplace renames old to new inside dir, failing if new exists.
func renameNoReplace(dir, old, new string) error {
	d, err := os.Open(dir) // #nosec G304 -- a root-only system folder, checked by the caller
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	fd := int(d.Fd()) // #nosec G115 -- a file descriptor
	return unix.Renameat2(fd, old, fd, new, unix.RENAME_NOREPLACE)
}

// maxList bounds one dpkg file list.
const maxList = 16 << 20

// readList calls fn for each line of info/<name>.list.
func readList(r Roots, name string, fn func(line string)) error {
	f, err := os.Open(r.info() + "/" + name) // #nosec G304 -- dpkg's own info folder; name from its listing or a validated package
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, maxList))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}

// PackageFiles lists the files of an installed package, "name:arch"
// (info/<name>:<arch>.list for Multi-Arch: same, else info/<name>.list).
func PackageFiles(r Roots, id string) []string {
	name, _, _ := strings.Cut(id, ":")
	var out []string
	for _, n := range []string{id + ".list", name + ".list"} {
		if err := readList(r, n, func(l string) { out = append(out, l) }); err == nil {
			return out
		}
	}
	return nil
}

// OwnedUnder maps each of paths that a package owns, itself or anything
// inside it, to that package. dpkg lists folders too, so /etc/foo owned
// by a package is found by its own line.
func OwnedUnder(r Roots, paths []string) (map[string]string, error) {
	out := map[string]string{}
	if len(paths) == 0 {
		return out, nil
	}
	ents, err := os.ReadDir(r.info())
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		pkg, ok := strings.CutSuffix(e.Name(), ".list")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		_ = readList(r, e.Name(), func(l string) {
			for _, p := range paths {
				if _, done := out[p]; !done && (l == p || strings.HasPrefix(l, p+"/")) {
					out[p] = pkg
				}
			}
		})
	}
	return out, nil
}
