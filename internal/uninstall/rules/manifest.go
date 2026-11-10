package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Operation IDs, archives and their manifests (SECURITY S16k, S16l). The
// broker writes a manifest beside each operation's archives and reads it
// back as untrusted before anything is restored.

var opRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidOp reports whether s is an operation ID: 32 lower-case hex digits,
// 16 random bytes the broker chose.
func ValidOp(s string) bool { return opRe.MatchString(s) }

// Entry kinds.
const (
	EntryDir      = "dir"      // a system leftover folder, moved
	EntryFile     = "file"     // a system leftover file, moved
	EntryLink     = "link"     // a system leftover link, moved (the link only)
	EntryConffile = "conffile" // a package's configuration file, copied before a purge
)

// Bounds of a manifest.
const (
	MaxManifest = 1 << 20
	MaxEntries  = 512
	MaxMoved    = 32 // leftovers moved by one operation
)

// Entry is one archived path.
type Entry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Package string `json:"package,omitempty"` // a conffile's package, "name" or "name:arch"
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Member  string `json:"member"`  // the tar member the path is stored as
	Archive string `json:"archive"` // "<index>.tar.zst" in the operation's folder
	SHA256  string `json:"sha256"`  // of the archive file
}

// Manifest is an operation's archives.
type Manifest struct {
	Op      string  `json:"op"`
	UID     uint32  `json:"uid"`  // who ran the uninstall
	Item    string  `json:"item"` // shown only
	Created int64   `json:"created"`
	Entries []Entry `json:"entries"`
}

// MovedMember is the name a moved leftover has beside itself while it is
// archived, and inside its archive.
func MovedMember(op string, i int) string { return fmt.Sprintf(".hearth-rm-%s-%d", op, i) }

// ArchiveName is an entry's archive file name.
func ArchiveName(i int) string { return fmt.Sprintf("%d.tar.zst", i) }

var (
	shaRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pkgArchRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,127}(:[a-z0-9][a-z0-9-]{0,31})?$`)
)

// ValidPackage reports whether s is a Debian package name, optionally with
// ":arch".
func ValidPackage(s string) bool { return pkgArchRe.MatchString(s) }

// CheckConffile checks that p may be a package's configuration file to
// copy or put back: clean, absolute, at least two levels inside /etc, no
// element starting with "-", nothing of Hearth's.
func CheckConffile(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	rest, ok := strings.CutPrefix(p, "/etc/")
	if !ok || !strings.Contains(rest, "/") {
		return fmt.Errorf("%s is not a file inside a folder of /etc", p)
	}
	if first, _, _ := strings.Cut(rest, "/"); first == "hearth" || first == "polkit-1" || first == "sudoers.d" {
		return fmt.Errorf("%s is never restored by Hearth", p)
	}
	return nil
}

// ParseManifest decodes and checks a manifest of operation op. Anything
// unexpected refuses the whole manifest, so nothing is restored from it.
func ParseManifest(op string, b []byte) (Manifest, error) {
	var m Manifest
	if !ValidOp(op) {
		return m, errors.New("bad operation ID")
	}
	if len(b) > MaxManifest {
		return m, errors.New("manifest too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if dec.More() {
		return Manifest{}, errors.New("manifest: trailing data")
	}
	if m.Op != op || m.Created <= 0 || len(m.Item) > 300 || m.UID > 1<<31 {
		return Manifest{}, errors.New("manifest: bad header")
	}
	if len(m.Entries) == 0 || len(m.Entries) > MaxEntries {
		return Manifest{}, errors.New("manifest: bad number of entries")
	}
	seen := map[string]bool{}
	moved := 0
	for i, e := range m.Entries {
		if err := checkEntry(op, i, e); err != nil {
			return Manifest{}, fmt.Errorf("manifest entry %d: %w", i, err)
		}
		if seen[e.Path] {
			return Manifest{}, fmt.Errorf("manifest: %s twice", e.Path)
		}
		seen[e.Path] = true
		if e.Kind != EntryConffile {
			moved++
		}
	}
	if moved > MaxMoved {
		return Manifest{}, errors.New("manifest: too many moved paths")
	}
	return m, nil
}

func checkEntry(op string, i int, e Entry) error {
	if e.Archive != ArchiveName(i) || !shaRe.MatchString(e.SHA256) || e.Size < 0 || e.UID > 1<<31 || e.GID > 1<<31 {
		return errors.New("bad archive fields")
	}
	switch e.Kind {
	case EntryDir, EntryFile, EntryLink:
		if _, _, err := CheckSystem(e.Path); err != nil {
			return err
		}
		if e.Member != MovedMember(op, i) || e.Package != "" {
			return errors.New("bad member")
		}
	case EntryConffile:
		if err := CheckConffile(e.Path); err != nil {
			return err
		}
		if e.Member != e.Path[1:] || !ValidPackage(e.Package) {
			return errors.New("bad member or package")
		}
	default:
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	return nil
}

// CheckMember checks one name a tar listing of e's archive printed: it must
// be e's member or inside it, relative and clean. A link or hard link is
// checked by its own name; tar keeps a link as a link.
func CheckMember(e Entry, name string) error {
	n := strings.TrimSuffix(name, "/")
	if n == "" || len(n) > 8192 || path.IsAbs(n) || path.Clean(n) != n || strings.HasPrefix(n, "../") || n == ".." {
		return fmt.Errorf("archive member %q is not a clean relative name", clip(name))
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return errors.New("archive member has a control character")
		}
	}
	if n != e.Member && !strings.HasPrefix(n, e.Member+"/") {
		return fmt.Errorf("archive member %q is outside %s", clip(name), e.Member)
	}
	if e.Kind != EntryDir && n != e.Member {
		return fmt.Errorf("archive member %q: a file's archive holds only the file", clip(name))
	}
	return nil
}

func clip(s string) string {
	if len(s) > 100 {
		return s[:100]
	}
	return s
}
