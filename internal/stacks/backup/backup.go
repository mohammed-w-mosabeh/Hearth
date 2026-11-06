// Package backup holds what a stack backup and restore share (ROADMAP 9.10,
// SECURITY S12d): archive and manifest names, the plan the broker writes
// for hearth-backup@ and hearth-restore@, and the fixed tar argv those
// units run. It does no I/O but the units' own steps (Run*), so the broker
// and the units check the same rules.
//
// An archive is /var/lib/hearth/backups/<stack>/<stamp>.tar.zst, member
// names being the real paths without their leading slash: the stack's
// folder, each named volume's _data and each bind included. Beside it,
// <stamp>.json is the manifest the broker writes, which says what the
// archive holds; restore reads only the manifest, never the archive, to
// decide anything. Restore writes back the named volumes and secrets.env
// only (owner's decision, 2026-11-06): binds stay in the archive, for the
// owner to copy back by hand, so the restore unit never sees home.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

const (
	// Dir holds one folder per stack, root's, 0700.
	Dir = "/var/lib/hearth/backups"
	// VolumesDir is Docker's local volume store. A volume Hearth backs up
	// has its data at VolumesDir/<name>/_data and nowhere else.
	VolumesDir = "/var/lib/docker/volumes"
	// StacksDir is the broker's stack store.
	StacksDir = "/var/lib/hearth/stacks"

	// PlanFile and RestoreFile are the units' instructions, in the stack's
	// backup folder, written by the broker just before it starts the unit.
	PlanFile    = ".plan.json"
	RestoreFile = ".restore.json"

	// KeepDefault, KeepMin and KeepMax bound retention.
	KeepDefault = 7
	KeepMin     = 1
	KeepMax     = 30

	// MaxPaths bounds the paths one archive holds.
	MaxPaths = 64

	// UnitTimeout bounds one backup or restore, as the units'
	// TimeoutStartSec does.
	UnitTimeout = 6 * time.Hour

	stampLayout = "20060102T150405Z"
)

var (
	archiveRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z\.tar\.zst$`)
	partialRe = regexp.MustCompile(`^\.[0-9]{8}T[0-9]{6}Z\.partial$`)
	volumeRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
	shaRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Stamp names a backup made at t.
func Stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

// ArchiveName is the archive of stamp; ManifestName its manifest;
// PartialName the file tar writes before the broker renames it.
func ArchiveName(stamp string) string  { return stamp + ".tar.zst" }
func ManifestName(stamp string) string { return stamp + ".json" }
func PartialName(stamp string) string  { return "." + stamp + ".partial" }

// ValidArchive reports whether name is an archive's name.
func ValidArchive(name string) bool { return archiveRe.MatchString(name) }

// ManifestOf is the manifest beside archive.
func ManifestOf(archive string) string {
	return ManifestName(strings.TrimSuffix(archive, ".tar.zst"))
}

// ValidVolume reports whether s is a Docker volume name.
func ValidVolume(s string) bool { return volumeRe.MatchString(s) }

// VolumeData is where a local volume keeps its data.
func VolumeData(name string) string { return VolumesDir + "/" + name + "/_data" }

// StackFolder is the stack's folder in the store.
func StackFolder(stack string) string { return StacksDir + "/" + stack }

// StackDir is the stack's backup folder.
func StackDir(stack string) string { return Dir + "/" + stack }

// Prune returns the archives to remove so that keep remain: the oldest.
// Names that are not archives are never returned.
func Prune(names []string, keep int) []string {
	var arch []string
	for _, n := range names {
		if ValidArchive(n) {
			arch = append(arch, n)
		}
	}
	slices.Sort(arch)
	if keep < KeepMin {
		keep = KeepMin
	}
	if len(arch) <= keep {
		return nil
	}
	return arch[:len(arch)-keep]
}

// Manifest is what the broker records beside an archive. It holds no
// secret value: secrets.env is in the archive, its keys are named here.
type Manifest struct {
	Version      int             `json:"version"`
	Stack        string          `json:"stack"`
	Archive      string          `json:"archive"`
	Created      string          `json:"created"` // RFC 3339, UTC
	SHA256       string          `json:"sha256"`  // of the archive
	Size         int64           `json:"size"`    // of the archive
	StoppedFirst bool            `json:"stopped_first"`
	Keep         int             `json:"keep"`
	SpecSHA256   string          `json:"spec_sha256"`
	Spec         json.RawMessage `json:"spec"`
	Volumes      []Volume        `json:"volumes"`
	Binds        []Bind          `json:"binds"`
	Secrets      []string        `json:"secrets"`
	// Warning says the archive may be inconsistent: files changed while
	// tar read them (a backup that did not stop the app first).
	Warning string `json:"warning,omitempty"`
}

// Volume is a named volume and the bytes its data held when measured.
type Volume struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Bind is a bind source: in the archive when Included, else Why says why
// not (another filesystem, not chosen).
type Bind struct {
	Service  string `json:"service"`
	Source   string `json:"source"`
	Real     string `json:"real"`
	Size     int64  `json:"size"`
	Partial  bool   `json:"partial,omitempty"` // Size is a lower bound
	OtherFS  bool   `json:"other_fs,omitempty"`
	Included bool   `json:"included"`
}

// MaxManifest bounds a manifest file: a spec and its lists.
const MaxManifest = spec.MaxSpec + 64<<10

// ParseManifest decodes a manifest strictly and checks it names archive
// of stack.
func ParseManifest(b []byte, stack, archive string) (Manifest, error) {
	var m Manifest
	if len(b) > MaxManifest {
		return m, errors.New("the manifest is too large")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.More() {
		return m, errors.New("the manifest cannot be read")
	}
	switch {
	case m.Version != 1:
		return m, errors.New("the manifest has an unknown version")
	case m.Stack != stack || m.Archive != archive:
		return m, errors.New("the manifest names another archive")
	case !shaRe.MatchString(m.SHA256) || m.Size <= 0:
		return m, errors.New("the manifest has no archive hash")
	}
	for _, v := range m.Volumes {
		if !ValidVolume(v.Name) {
			return m, errors.New("the manifest names a bad volume")
		}
	}
	return m, nil
}

// Plan is hearth-backup@'s instructions: tar writes Output (a partial
// name) in the stack's backup folder, holding Paths.
type Plan struct {
	Stack  string   `json:"stack"`
	Output string   `json:"output"`
	Paths  []string `json:"paths"`
}

// RestorePlan is hearth-restore@'s instructions: check the archive's hash,
// empty each volume's data, then extract those volumes and, if Secrets,
// the stack's secrets.env.
type RestorePlan struct {
	Stack   string   `json:"stack"`
	Archive string   `json:"archive"`
	SHA256  string   `json:"sha256"`
	Volumes []string `json:"volumes"`
	Secrets bool     `json:"secrets"`
}

// cleanAbs checks p is an absolute, clean path tar can take as an operand
// once its leading slash is gone: no control character, no part starting
// with "-" (an option) and not "/".
func cleanAbs(p string) error {
	switch {
	case !utf8.ValidString(p) || len(p) > 4096:
		return errors.New("not a path")
	case !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/":
		return fmt.Errorf("%q is not an absolute, clean path", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return errors.New("a path holds a control character")
		}
	}
	for _, part := range strings.Split(p[1:], "/") {
		if strings.HasPrefix(part, "-") {
			return fmt.Errorf("%q has a part starting with -", p)
		}
	}
	return nil
}

func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// Check validates a backup plan. Paths are the stack's own folder, volume
// data folders, and bind sources; none may hold or lie in the backups.
func (p Plan) Check() error {
	if !spec.ValidName(p.Stack) {
		return errors.New("not a stack name")
	}
	if !partialRe.MatchString(p.Output) {
		return errors.New("not an output name")
	}
	if len(p.Paths) == 0 || len(p.Paths) > MaxPaths {
		return errors.New("a plan holds 1 to 64 paths")
	}
	seen := map[string]bool{}
	for _, x := range p.Paths {
		if err := cleanAbs(x); err != nil {
			return err
		}
		if seen[x] {
			return fmt.Errorf("%s is listed twice", x)
		}
		seen[x] = true
		if within(Dir, x) || within(x, Dir) {
			return fmt.Errorf("%s holds or lies in the backups", x)
		}
		if within(x, StacksDir) && x != StackFolder(p.Stack) {
			return fmt.Errorf("%s is another stack's", x)
		}
		if within(x, VolumesDir) {
			rest := strings.TrimPrefix(x, VolumesDir+"/")
			name, tail, _ := strings.Cut(rest, "/")
			if !ValidVolume(name) || tail != "_data" {
				return fmt.Errorf("%s is not a volume's data", x)
			}
		}
	}
	if !seen[StackFolder(p.Stack)] {
		return errors.New("the plan leaves out the stack's own folder")
	}
	return nil
}

// Check validates a restore plan.
func (p RestorePlan) Check() error {
	switch {
	case !spec.ValidName(p.Stack):
		return errors.New("not a stack name")
	case !ValidArchive(p.Archive):
		return errors.New("not an archive name")
	case !shaRe.MatchString(p.SHA256):
		return errors.New("not a hash")
	case len(p.Volumes) > MaxPaths:
		return errors.New("too many volumes")
	case len(p.Volumes) == 0 && !p.Secrets:
		return errors.New("nothing to restore")
	}
	seen := map[string]bool{}
	for _, v := range p.Volumes {
		if !ValidVolume(v) || seen[v] {
			return errors.New("a bad or repeated volume")
		}
		seen[v] = true
	}
	return nil
}

// TarCreate is the fixed argv after "tar": zstd, member names relative to
// /, numeric owners, sparse files kept, never crossing into another mount,
// and no link followed. Paths are checked by Plan.Check.
func TarCreate(p Plan) []string {
	args := []string{"--create", "--zstd", "--file=" + StackDir(p.Stack) + "/" + p.Output, "--directory=/",
		"--numeric-owner", "--sparse", "--one-file-system", "--warning=no-file-ignored", "--"}
	for _, x := range p.Paths {
		args = append(args, strings.TrimPrefix(x, "/"))
	}
	return args
}

// TarExtract is the fixed argv after "tar" for a restore: only the named
// members, owners and modes as archived, into /. The unit's sandbox lets it
// write the volume store and the stack's folder only.
func TarExtract(p RestorePlan) []string {
	args := []string{"--extract", "--zstd", "--file=" + StackDir(p.Stack) + "/" + p.Archive, "--directory=/",
		"--numeric-owner", "--same-owner", "--same-permissions", "--"}
	for _, v := range p.Volumes {
		args = append(args, strings.TrimPrefix(VolumeData(v), "/"))
	}
	if p.Secrets {
		args = append(args, strings.TrimPrefix(StackFolder(p.Stack), "/")+"/secrets.env")
	}
	return args
}

// CheckTarCreate validates a tar argv as TarCreate makes it, for safeexec.
func CheckTarCreate(args []string) error {
	i := slices.Index(args, "--")
	if i != 8 || len(args) < 10 {
		return errors.New("not a backup's tar arguments")
	}
	file, ok := strings.CutPrefix(args[2], "--file="+Dir+"/")
	if !ok {
		return errors.New("tar must write in the backups")
	}
	stack, out, ok := strings.Cut(file, "/")
	if !ok {
		return errors.New("tar must write in a stack's backups")
	}
	p := Plan{Stack: stack, Output: out}
	for _, a := range args[9:] {
		p.Paths = append(p.Paths, "/"+a)
	}
	if err := p.Check(); err != nil {
		return err
	}
	if !slices.Equal(args, TarCreate(p)) {
		return errors.New("not a backup's tar arguments")
	}
	return nil
}

// CheckTarExtract validates a tar argv as TarExtract makes it.
func CheckTarExtract(args []string) error {
	i := slices.Index(args, "--")
	if i != 7 || len(args) < 9 {
		return errors.New("not a restore's tar arguments")
	}
	file, ok := strings.CutPrefix(args[2], "--file="+Dir+"/")
	if !ok {
		return errors.New("tar must read from the backups")
	}
	stack, archive, ok := strings.Cut(file, "/")
	if !ok {
		return errors.New("tar must read a stack's archive")
	}
	p := RestorePlan{Stack: stack, Archive: archive, SHA256: strings.Repeat("0", 64)}
	for _, a := range args[8:] {
		if a == StackFolder(stack)[1:]+"/secrets.env" && !p.Secrets {
			p.Secrets = true
			continue
		}
		name, ok := strings.CutPrefix(a, VolumesDir[1:]+"/")
		name, ok2 := strings.CutSuffix(name, "/_data")
		if !ok || !ok2 {
			return errors.New("not a restore's member")
		}
		p.Volumes = append(p.Volumes, name)
	}
	if err := p.Check(); err != nil {
		return err
	}
	if !slices.Equal(args, TarExtract(p)) {
		return errors.New("not a restore's tar arguments")
	}
	return nil
}
