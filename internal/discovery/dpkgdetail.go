package discovery

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// One Debian package in detail (SPEC 4: files, deps, reverse deps), from
// the same world-readable files as the list.

// maxFiles bounds the file list of one package (a -doc or -data package
// can have tens of thousands).
const maxFiles = 5000

// PackageDetail is one installed package with its files and relations.
type PackageDetail struct {
	Package
	Deps           []string `json:"deps"`         // names it depends on (alternatives included)
	ReverseDeps    []string `json:"reverse_deps"` // installed packages that depend on it
	Files          []string `json:"files"`
	FilesTruncated bool     `json:"files_truncated,omitempty"`
}

// depNameRe is the package name at the start of one dependency.
var depNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*`)

// DepNames lists the package names in a Depends field: "a (>= 1), b | c,
// d:any" is a, b, c, d. Duplicates are dropped; order is kept.
func DepNames(field string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dep := range strings.Split(field, ",") {
		for _, alt := range strings.Split(dep, "|") {
			n := depNameRe.FindString(strings.TrimSpace(alt))
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// Detail returns the installed package id ("name:arch"), or false.
func (d *Dpkg) Detail(id string) (PackageDetail, bool, error) {
	pkgs, err := d.Packages()
	if err != nil {
		return PackageDetail{}, false, err
	}
	var det PackageDetail
	found := false
	for _, p := range pkgs {
		if p.ID() == id {
			det.Package, found = p, true
			break
		}
	}
	if !found {
		return det, false, nil
	}
	det.Deps = DepNames(det.Depends)
	det.ReverseDeps = []string{}
	for _, p := range pkgs {
		if p.Name == det.Name || p.State != "installed" {
			continue
		}
		for _, n := range DepNames(p.Depends) {
			if n == det.Name {
				det.ReverseDeps = append(det.ReverseDeps, p.Name)
				break
			}
		}
	}
	sort.Strings(det.ReverseDeps)
	det.ReverseDeps = compactStrings(det.ReverseDeps)
	det.Files, det.FilesTruncated = d.files(det.Package)
	return det, true, nil
}

func compactStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// files reads dpkg's file list of p: info/<name>:<arch>.list for a
// Multi-Arch: same package, else info/<name>.list. Directories are left
// out.
func (d *Dpkg) files(p Package) ([]string, bool) {
	info := filepath.Join(filepath.Dir(d.cfg.Status), "info")
	out := []string{}
	var f *os.File
	var err error
	for _, name := range []string{p.Name + ":" + p.Arch + ".list", p.Name + ".list"} {
		if f, err = os.Open(filepath.Join(info, name)); err == nil { // #nosec G304 -- name is from dpkg's own status file
			break
		}
	}
	if err != nil {
		return out, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		path := sc.Text()
		if path == "" || path == "/." {
			continue
		}
		if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
			continue
		}
		if len(out) == maxFiles {
			return out, true
		}
		out = append(out, strings.ToValidUTF8(path, "�"))
	}
	return out, false
}

// HandleDetail serves GET /api/v1/pkgs/apt/{id}.
func (d *Dpkg) HandleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !strings.Contains(id, ":") || len(id) > 200 {
		http.Error(w, "id must be name:arch", http.StatusBadRequest)
		return
	}
	det, ok, err := d.Detail(id)
	switch {
	case err != nil:
		http.Error(w, "the package database could not be read", http.StatusServiceUnavailable)
		return
	case !ok:
		http.Error(w, "no such package", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, det)
}
