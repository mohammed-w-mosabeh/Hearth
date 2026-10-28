package discovery

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Packages from managers other than dpkg (SPEC 4, ROADMAP 3.2), read from
// their own files (or snapd's API) without running them. Each lister is
// cheap and runs on demand.

// ExtraPackage is one package of a non-dpkg manager.
type ExtraPackage struct {
	ID        string   `json:"id"` // the action target
	Name      string   `json:"name"`
	Version   string   `json:"version,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Kind      string   `json:"kind,omitempty"`     // flatpak: app or runtime; snap: its type
	Location  string   `json:"location,omitempty"` // installation, prefix or file
	SizeBytes int64    `json:"size_bytes,omitempty"`
	UsedBy    []string `json:"used_by,omitempty"`
	Bins      []string `json:"bins,omitempty"` // cargo: the programs it installed
	Removable bool     `json:"removable"`
	// Why, when not removable (web/src/lib/actions.ts reasons).
	Why string `json:"why,omitempty"`
}

// ExtraList is one manager's packages.
type ExtraList struct {
	Manager   string         `json:"manager"`
	Available bool           `json:"available"` // the manager is installed
	Packages  []ExtraPackage `json:"packages"`
}

// Reasons an extra package is not removable from Hearth.
const (
	WhyInUse   = "in-use"  // a runtime installed apps need
	WhySystem  = "system"  // a snap the system needs (base, snapd, kernel)
	WhyManager = "manager" // the package manager itself
	WhyRoot    = "root"    // installed for all users: needs sudo
	WhyPip     = "pip"     // pip --user: Hearth only lists these
)

// ExtraConfig locates the managers' files.
type ExtraConfig struct {
	Home       string // the user's home
	Getenv     func(string) string
	SnapSocket string   // default /run/snapd.socket
	Roots      []string // flatpak system installations; default /var/lib/flatpak
}

func (c ExtraConfig) env(k string) string {
	if c.Getenv == nil {
		return ""
	}
	return c.Getenv(k)
}

func (c ExtraConfig) dataHome() string {
	if d := c.env("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(c.Home, ".local/share")
}

// maxEntries bounds what one lister reads.
const maxEntries = 5000

// --- Flatpak ---

// FlatpakRefRe is one part of a flatpak ref (ID, arch or branch).
var FlatpakRefRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)

// Flatpak lists apps and runtimes of the system installation(s) and the
// user's. ID is "<installation>/<kind>/<id>/<arch>/<branch>", installation
// being "system" or "user".
func Flatpak(c ExtraConfig) ExtraList {
	out := ExtraList{Manager: "flatpak", Packages: []ExtraPackage{}}
	_, err := os.Stat("/usr/bin/flatpak")
	out.Available = err == nil
	roots := flatpakRoots(c)
	type inst struct{ name, dir string }
	insts := []inst{{"user", filepath.Join(c.dataHome(), "flatpak")}}
	for _, r := range roots {
		insts = append(insts, inst{"system", r})
	}
	usedBy := map[string][]string{} // "installation/id/arch/branch" of a runtime -> apps
	for _, in := range insts {
		for _, kind := range []string{"app", "runtime"} {
			ids, _ := os.ReadDir(filepath.Join(in.dir, kind))
			for _, id := range ids {
				arches, _ := os.ReadDir(filepath.Join(in.dir, kind, id.Name()))
				for _, arch := range arches {
					branches, _ := os.ReadDir(filepath.Join(in.dir, kind, id.Name(), arch.Name()))
					for _, br := range branches {
						if len(out.Packages) >= maxEntries {
							break
						}
						active := filepath.Join(in.dir, kind, id.Name(), arch.Name(), br.Name(), "active")
						if _, err := os.Stat(active); err != nil || !FlatpakRefRe.MatchString(id.Name()) ||
							!FlatpakRefRe.MatchString(arch.Name()) || !FlatpakRefRe.MatchString(br.Name()) {
							continue
						}
						p := ExtraPackage{
							ID:   strings.Join([]string{in.name, kind, id.Name(), arch.Name(), br.Name()}, "/"),
							Name: id.Name(), Kind: kind, Location: in.name, Version: br.Name(), Removable: true,
						}
						name, version := metainfo(filepath.Join(active, "files/share/metainfo", id.Name()+".metainfo.xml"))
						if name == "" {
							name, version = metainfo(filepath.Join(active, "files/share/appdata", id.Name()+".appdata.xml"))
						}
						if name != "" {
							p.Summary = name
						}
						if version != "" {
							p.Version = version
						}
						if kind == "app" {
							if rt := keyfileValue(filepath.Join(active, "metadata"), "Application", "runtime"); rt != "" {
								// An app's runtime is in its own installation, or the system's.
								usedBy[in.name+"/"+rt] = append(usedBy[in.name+"/"+rt], id.Name())
								if in.name != "system" {
									usedBy["system/"+rt] = append(usedBy["system/"+rt], id.Name())
								}
							}
						}
						out.Packages = append(out.Packages, p)
					}
				}
			}
		}
	}
	// Extensions of a runtime in use (GL drivers, codecs, Compat.i386,
	// Locale) are in use by the same apps: "<runtime id>.<name>", any
	// branch, same architecture.
	extUsers := map[string][]string{} // "<runtime id>/<arch>" -> apps
	for k, apps := range usedBy {
		f := strings.Split(k, "/") // installation/id/arch/branch
		extUsers[f[1]+"/"+f[2]] = append(extUsers[f[1]+"/"+f[2]], apps...)
	}
	for i, p := range out.Packages {
		if p.Kind != "runtime" {
			continue
		}
		f := strings.SplitN(p.ID, "/", 5)
		apps := usedBy[f[0]+"/"+f[2]+"/"+f[3]+"/"+f[4]]
		if len(apps) == 0 {
			for k, users := range extUsers {
				rt, arch, _ := strings.Cut(k, "/")
				if arch == f[3] && strings.HasPrefix(f[2], rt+".") {
					apps = append(apps, users...)
				}
			}
		}
		if len(apps) > 0 {
			sort.Strings(apps)
			out.Packages[i].UsedBy = slices.Compact(apps)
			out.Packages[i].Removable, out.Packages[i].Why = false, WhyInUse
		}
	}
	sortExtra(out.Packages)
	return out
}

// flatpakRoots are the system installations: c.Roots, default
// /var/lib/flatpak.
func flatpakRoots(c ExtraConfig) []string {
	if c.Roots == nil {
		return []string{"/var/lib/flatpak"}
	}
	return c.Roots
}

// metainfo reads an AppStream file's first <name> (untranslated) and its
// newest release's version.
func metainfo(path string) (name, version string) {
	f, err := os.Open(path) // #nosec G304 -- under a flatpak deploy directory
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	dec := xml.NewDecoder(io.LimitReader(f, 1<<20))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return name, version
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case t.Name.Local == "name" && depth == 2 && name == "" && attr(t, "lang") == "":
				var s string
				if dec.DecodeElement(&s, &t) == nil {
					name = clipText(strings.TrimSpace(s), 200)
				}
				depth--
			case t.Name.Local == "release" && version == "":
				version = clipText(attr(t, "version"), 100)
			}
		case xml.EndElement:
			depth--
		}
		if name != "" && version != "" {
			return name, version
		}
	}
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func clipText(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}

// keyfileValue reads key from [group] of a small INI-style file.
func keyfileValue(path, group, key string) string {
	b, err := readLimited(path, 64<<10)
	if err != nil {
		return ""
	}
	in := group == "" // a file without groups (go env)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "["+group+"]"
			continue
		}
		if k, v, ok := strings.Cut(line, "="); in && ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// --- Snap ---

// systemSnaps are snap types the system runs on.
var systemSnaps = map[string]bool{"base": true, "core": true, "os": true, "snapd": true, "kernel": true, "gadget": true}

// Snap lists installed snaps through snapd's API (GET /v2/snaps, open to
// every user).
func Snap(ctx context.Context, c ExtraConfig) ExtraList {
	out := ExtraList{Manager: "snap", Packages: []ExtraPackage{}}
	var r struct {
		Result []struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			Summary       string `json:"summary"`
			Type          string `json:"type"`
			Channel       string `json:"tracking-channel"`
			InstalledSize int64  `json:"installed-size"`
		} `json:"result"`
	}
	if err := SnapdJSON(ctx, c.SnapSocket, http.MethodGet, "/v2/snaps", nil, &r); err != nil {
		return out
	}
	out.Available = true
	for _, s := range r.Result {
		if len(out.Packages) >= maxEntries || !SnapNameRe.MatchString(s.Name) {
			continue
		}
		p := ExtraPackage{ID: s.Name, Name: s.Name, Version: clipText(s.Version, 100), Summary: clipText(s.Summary, 300),
			Kind: s.Type, Location: s.Channel, SizeBytes: s.InstalledSize, Removable: true}
		if systemSnaps[s.Type] {
			p.Removable, p.Why = false, WhySystem
		}
		out.Packages = append(out.Packages, p)
	}
	sortExtra(out.Packages)
	return out
}

// SnapNameRe is a snap name.
var SnapNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// SnapdJSON makes one request to snapd and decodes its answer into out.
func SnapdJSON(ctx context.Context, socket, method, path string, body io.Reader, out any, hdr ...string) error {
	if socket == "" {
		socket = "/run/snapd.socket"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, method, "http://snapd"+path, body)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Result struct {
				Message string `json:"message"`
			} `json:"result"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Result.Message == "" {
			e.Result.Message = resp.Status
		}
		return errors.New("snapd: " + e.Result.Message)
	}
	return json.Unmarshal(b, out)
}

// --- pip --user ---

// Pip lists packages pip installed in the user's site-packages
// (~/.local/lib/python3.*/site-packages). Hearth does not remove them:
// on Debian, pip would need --break-system-packages.
func Pip(c ExtraConfig) ExtraList {
	out := ExtraList{Manager: "pip", Packages: []ExtraPackage{}}
	_, err := os.Stat("/usr/bin/pip3")
	out.Available = err == nil
	sites, _ := filepath.Glob(filepath.Join(c.Home, ".local/lib/python3*/site-packages"))
	for _, site := range sites {
		infos, _ := filepath.Glob(filepath.Join(site, "*.dist-info"))
		for _, info := range infos {
			if len(out.Packages) >= maxEntries {
				break
			}
			meta := headers(filepath.Join(info, "METADATA"), "Name", "Version", "Summary")
			if meta["Name"] == "" {
				continue
			}
			p := ExtraPackage{ID: strings.ToLower(meta["Name"]), Name: meta["Name"], Version: clipText(meta["Version"], 100),
				Summary: clipText(meta["Summary"], 300), Location: site, Why: WhyPip,
				Bins: pipScripts(filepath.Join(info, "RECORD"), site, filepath.Join(c.Home, ".local/bin"))}
			out.Packages = append(out.Packages, p)
		}
	}
	sortExtra(out.Packages)
	return out
}

// pipScripts are the programs a package's RECORD put in bin
// ("../../../bin/flask,sha256=...,208", relative to site-packages).
func pipScripts(record, site, bin string) []string {
	b, err := readLimited(record, 1<<20)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		rel, _, _ := strings.Cut(line, ",")
		if p := filepath.Clean(filepath.Join(site, rel)); filepath.Dir(p) == bin && GoBinNameRe.MatchString(filepath.Base(p)) {
			out = append(out, filepath.Base(p))
		}
	}
	return out
}

// headers reads RFC 822-style headers (up to the first blank line).
func headers(path string, keys ...string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path) // #nosec G304 -- under a site-packages directory
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	for sc.Scan() && sc.Text() != "" {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && want[k] && out[k] == "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// --- npm -g ---

// NpmNameRe is an npm package name, scoped or not.
var NpmNameRe = regexp.MustCompile(`^(@[a-z0-9~][a-z0-9-._~]{0,100}/)?[a-z0-9~][a-z0-9-._~]{0,213}$`)

// NpmPrefixes are the global prefixes npm may use: NPM_CONFIG_PREFIX,
// prefix= in ~/.npmrc, each nvm Node version, /usr/local and /usr.
func NpmPrefixes(c ExtraConfig) []string {
	var out []string
	add := func(p string) {
		if filepath.IsAbs(p) {
			p = filepath.Clean(p)
			for _, q := range out {
				if q == p {
					return
				}
			}
			out = append(out, p)
		}
	}
	add(c.env("NPM_CONFIG_PREFIX"))
	if b, err := readLimited(filepath.Join(c.Home, ".npmrc"), 64<<10); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "prefix" {
				add(strings.Replace(strings.TrimSpace(v), "~", c.Home, 1))
			}
		}
	}
	nvm := c.env("NVM_DIR")
	if nvm == "" {
		nvm = filepath.Join(c.Home, ".nvm")
	}
	versions, _ := filepath.Glob(filepath.Join(nvm, "versions/node/*"))
	for _, v := range versions {
		add(v)
	}
	add("/usr/local")
	add("/usr")
	return out
}

// Npm lists global npm packages. ID is "<prefix>::<name>". Packages in a
// prefix the user does not own need sudo; npm and corepack are the
// manager itself.
func Npm(c ExtraConfig) ExtraList {
	out := ExtraList{Manager: "npm", Packages: []ExtraPackage{}}
	uid := os.Getuid()
	for _, prefix := range NpmPrefixes(c) {
		mods := filepath.Join(prefix, "lib/node_modules")
		entries, err := os.ReadDir(mods)
		if err != nil {
			continue
		}
		out.Available = true
		owned := false
		if fi, err := os.Stat(mods); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid {
				owned = true
			}
		}
		_, npmErr := os.Stat(filepath.Join(prefix, "bin/npm"))
		var names []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "@") {
				scoped, _ := os.ReadDir(filepath.Join(mods, e.Name()))
				for _, s := range scoped {
					names = append(names, e.Name()+"/"+s.Name())
				}
			} else if !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		for _, name := range names {
			if len(out.Packages) >= maxEntries || !NpmNameRe.MatchString(name) {
				continue
			}
			var pj struct {
				Version     string `json:"version"`
				Description string `json:"description"`
			}
			if b, err := readLimited(filepath.Join(mods, name, "package.json"), 1<<20); err == nil {
				_ = json.Unmarshal(b, &pj)
			}
			p := ExtraPackage{ID: prefix + "::" + name, Name: name, Version: clipText(pj.Version, 100),
				Summary: clipText(pj.Description, 300), Location: prefix, Removable: true}
			switch {
			case name == "npm" || name == "corepack":
				p.Removable, p.Why = false, WhyManager
			case !owned:
				p.Removable, p.Why = false, WhyRoot
			case npmErr != nil:
				p.Removable, p.Why = false, WhyManager
			}
			out.Packages = append(out.Packages, p)
		}
	}
	sortExtra(out.Packages)
	return out
}

// --- cargo ---

// CrateNameRe is a crate name.
var CrateNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// CargoHome is $CARGO_HOME or ~/.cargo.
func CargoHome(c ExtraConfig) string {
	if h := c.env("CARGO_HOME"); filepath.IsAbs(h) {
		return h
	}
	return filepath.Join(c.Home, ".cargo")
}

// Cargo lists crates installed with cargo install (.crates2.json).
func Cargo(c ExtraConfig) ExtraList {
	out := ExtraList{Manager: "cargo", Packages: []ExtraPackage{}}
	home := CargoHome(c)
	_, err := os.Stat(filepath.Join(home, "bin/cargo"))
	out.Available = err == nil
	b, err := readLimited(filepath.Join(home, ".crates2.json"), 4<<20)
	if err != nil {
		return out
	}
	var v struct {
		Installs map[string]struct {
			Bins []string `json:"bins"`
		} `json:"installs"`
	}
	if json.Unmarshal(b, &v) != nil {
		return out
	}
	for key, inst := range v.Installs { // "name version (source)"
		f := strings.Fields(key)
		if len(f) < 2 || !CrateNameRe.MatchString(f[0]) || len(out.Packages) >= maxEntries {
			continue
		}
		p := ExtraPackage{ID: f[0], Name: f[0], Version: clipText(f[1], 100), Location: home, Removable: out.Available,
			Summary: clipText(strings.Join(inst.Bins, ", "), 300), Bins: inst.Bins}
		if !out.Available {
			p.Why = WhyManager
		}
		out.Packages = append(out.Packages, p)
	}
	sortExtra(out.Packages)
	return out
}

// --- Go binaries ---

// GoBin is the directory go install writes to: $GOBIN, else the first
// $GOPATH entry's bin, else ~/go/bin (also from ~/.config/go/env).
func GoBin(c ExtraConfig) string {
	goenv := func(k string) string {
		if v := c.env(k); v != "" {
			return v
		}
		return keyfileValue(filepath.Join(c.Home, ".config/go/env"), "", k)
	}
	if d := goenv("GOBIN"); filepath.IsAbs(d) {
		return d
	}
	if p, _, _ := strings.Cut(goenv("GOPATH"), ":"); filepath.IsAbs(p) {
		return filepath.Join(p, "bin")
	}
	return filepath.Join(c.Home, "go/bin")
}

// GoBinaries lists the Go programs in GoBin, with the module and version
// they were built from. ID is the file name; removing moves it to the
// trash.
func GoBinaries(c ExtraConfig) ExtraList {
	dir := GoBin(c)
	out := ExtraList{Manager: "go", Packages: []ExtraPackage{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	out.Available = true
	_, gioErr := os.Stat("/usr/bin/gio")
	for _, e := range entries {
		if len(out.Packages) >= 500 || !e.Type().IsRegular() || !GoBinNameRe.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		bi, err := buildinfo.ReadFile(path)
		if err != nil {
			continue // not a Go program
		}
		p := ExtraPackage{ID: e.Name(), Name: e.Name(), Version: clipText(bi.Main.Version, 100),
			Summary: clipText(bi.Main.Path, 300), Location: path, Removable: gioErr == nil}
		if fi, err := e.Info(); err == nil {
			p.SizeBytes = fi.Size()
		}
		if gioErr != nil {
			p.Why = WhyManager
		}
		out.Packages = append(out.Packages, p)
	}
	sortExtra(out.Packages)
	return out
}

// GoBinNameRe is a program file name.
var GoBinNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,127}$`)

func sortExtra(ps []ExtraPackage) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name)
		}
		return ps[i].ID < ps[j].ID
	})
}

// HandleExtra serves GET /api/v1/pkgs/{manager} for the managers above.
func HandleExtra(c ExtraConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var l ExtraList
		switch r.PathValue("manager") {
		case "flatpak":
			l = Flatpak(c)
		case "snap":
			l = Snap(r.Context(), c)
		case "pip":
			l = Pip(c)
		case "npm":
			l = Npm(c)
		case "cargo":
			l = Cargo(c)
		case "go":
			l = GoBinaries(c)
		default:
			http.Error(w, "unknown package manager", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, l)
	}
}
