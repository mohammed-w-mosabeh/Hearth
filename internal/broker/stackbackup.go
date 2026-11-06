package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/backup"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Stack backups (ROADMAP 9.10, SECURITY S12d). The broker decides what an
// archive holds and writes it down as a plan; hearth-backup@<stack> runs
// the fixed tar on it, reading with CAP_DAC_READ_SEARCH only and writing
// only in the stack's backup folder. The broker then hashes the archive and
// records a manifest beside it, and keeps the last N. A restore is planned
// from the manifest alone, needs the preview's token and the stack's name
// typed back, and hearth-restore@<stack> checks the archive's hash before it
// writes the stack's named volumes and secrets.env, and nothing else: binds
// stay in the archive (owner's decision, 2026-11-06).
const (
	ActionStackBackup  = "org.hearth.stack.backup"  // auth_admin_keep
	ActionStackRestore = "org.hearth.stack.restore" // auth_admin

	BackupUnitPrefix  = "hearth-backup@"
	RestoreUnitPrefix = "hearth-restore@"

	// restoreTTL keeps a restore preview's token: longer than hearthd's
	// two-minute preview, so the dialog has time.
	restoreTTL = 10 * time.Minute
	// freeMargin is left free on the backups' filesystem.
	freeMargin = 256 << 20
)

// BackupUnit and RestoreUnit run one backup or restore of name.
func BackupUnit(name string) string  { return BackupUnitPrefix + name + ".service" }
func RestoreUnit(name string) string { return RestoreUnitPrefix + name + ".service" }

// VolumeInfo is what a backup needs to know of a named volume. Plain means
// the local driver with no options, its data at backup.VolumeData(name):
// the only kind Hearth backs up or restores.
type VolumeInfo struct {
	Exists bool
	Plain  bool
	Size   int64 // -1 when the Engine cannot tell
}

// BackupVolumes reads volumes for backups (the Engine in production).
type BackupVolumes interface {
	BackupVolumes(ctx context.Context, names []string) (map[string]VolumeInfo, error)
}

// StackBackupParams is stack.backup's params, and stack.backup.preview's
// (which ignores StopFirst). Include names, by real path, the binds on
// another filesystem to back up too; Keep is how many archives stay.
type StackBackupParams struct {
	Name      string   `json:"name"`
	StopFirst bool     `json:"stop_first"`
	Keep      int      `json:"keep"`
	Include   []string `json:"include"`
}

// BackupVolume is one named volume of a backup preview.
type BackupVolume struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Included bool   `json:"included"`
	Why      string `json:"why,omitempty"`
}

// BackupPreview is what stack.backup.preview returns: sizes first.
type BackupPreview struct {
	Name     string         `json:"name"`
	Running  bool           `json:"running"`
	Volumes  []BackupVolume `json:"volumes"`
	Binds    []backup.Bind  `json:"binds"`
	Secrets  []string       `json:"secrets"`
	Estimate int64          `json:"estimate"`
	Partial  bool           `json:"partial"`
	Free     int64          `json:"free"`
	Enough   bool           `json:"enough"`
	Keep     int            `json:"keep"`
	Archives []string       `json:"archives"`
	Prune    []string       `json:"prune"`
}

// BackupResult is what stack.backup returns.
type BackupResult struct {
	Archive string   `json:"archive"`
	Size    int64    `json:"size"`
	Warning string   `json:"warning,omitempty"`
	Removed []string `json:"removed"`
}

// BackupEntry is one archive in stack.backup.list. The spec stays out.
type BackupEntry struct {
	Archive      string          `json:"archive"`
	Created      string          `json:"created"`
	Size         int64           `json:"size"`
	StoppedFirst bool            `json:"stopped_first"`
	Keep         int             `json:"keep"`
	Volumes      []backup.Volume `json:"volumes"`
	Binds        []backup.Bind   `json:"binds"`
	Secrets      []string        `json:"secrets"`
	Warning      string          `json:"warning,omitempty"`
}

// BackupList is stack.backup.list's answer.
type BackupList struct {
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	Archives []BackupEntry `json:"archives"`
}

// StackRestorePreviewParams is stack.restore.preview's params.
type StackRestorePreviewParams struct {
	Name    string `json:"name"`
	Archive string `json:"archive"`
}

// RestoreVolume is a volume a restore overwrites.
type RestoreVolume struct {
	Name     string `json:"name"`
	Archived int64  `json:"archived"`
	Current  int64  `json:"current"` // -1 unknown
	Exists   bool   `json:"exists"`
}

// RestorePreview is what a restore would do, and its token.
type RestorePreview struct {
	Name        string          `json:"name"`
	Archive     string          `json:"archive"`
	Created     string          `json:"created"`
	Token       string          `json:"token"`
	Volumes     []RestoreVolume `json:"volumes"`
	Secrets     []string        `json:"secrets"`
	NewStack    bool            `json:"new_stack"`
	SpecChanged bool            `json:"spec_changed"`
	Flags       []string        `json:"flags"`
	Binds       []backup.Bind   `json:"binds"` // in the archive, not restored
	Running     bool            `json:"running"`
	Warning     string          `json:"warning,omitempty"`
}

// StackRestoreParams is stack.restore's params.
type StackRestoreParams struct {
	Name      string   `json:"name"`
	Archive   string   `json:"archive"`
	Token     string   `json:"token"`
	TypedName string   `json:"typed_name"`
	AckFlags  []string `json:"ack_flags"`
}

type restoreGrant struct {
	name, archive, sha, current string
	at                          time.Time
}

type restoreGrants struct {
	mu  sync.Mutex
	m   map[string]restoreGrant
	now func() time.Time
}

func (g *restoreGrants) put(r restoreGrant) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.m {
		if g.now().Sub(v.at) > restoreTTL {
			delete(g.m, k)
		}
	}
	if len(g.m) >= 32 {
		clear(g.m)
	}
	r.at = g.now()
	g.m[tok] = r
	return tok, nil
}

// get returns the grant of tok; take also forgets it, so it is used once.
func (g *restoreGrants) get(tok string, take bool) (restoreGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.m[tok]
	if take {
		delete(g.m, tok)
	}
	if !ok || g.now().Sub(r.at) > restoreTTL {
		return restoreGrant{}, false
	}
	return r, true
}

func (s *stacks) backupRoots() backup.Roots { return backup.Roots{Backups: s.c.Backups} }

func (s *stacks) backupsDir() string {
	if s.c.Backups == "" {
		return backup.Dir
	}
	return s.c.Backups
}

// backupMethods are stack.backup.preview, stack.backup, stack.backup.list,
// stack.restore.preview and stack.restore.
func (s *stacks) backupMethods() []Method {
	return []Method{
		{Name: "stack.backup.preview", Timeout: time.Minute,
			Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
				v, err := decodeBackup(p)
				if err != nil {
					return nil, err
				}
				pl, err := s.planBackup(ctx, v)
				return pl.preview, err
			}},
		{Name: "stack.backup", Privileged: true, Action: ActionStackBackup, Timeout: backup.UnitTimeout + 15*time.Minute,
			Check: func(ctx context.Context, p json.RawMessage) error {
				v, err := decodeBackup(p)
				if err != nil {
					return err
				}
				pl, err := s.planBackup(ctx, v)
				if err == nil && !pl.preview.Enough {
					err = badParam("not enough free space for the backup")
				}
				return pre(err)
			},
			Handle: s.backup},
		{Name: "stack.backup.list", Handle: s.backupList},
		{Name: "stack.restore.preview", Timeout: time.Minute, Handle: s.restorePreview},
		{Name: "stack.restore", Privileged: true, Action: ActionStackRestore, Timeout: backup.UnitTimeout + StackTimeout,
			Check: func(ctx context.Context, p json.RawMessage) error {
				_, err := s.checkRestore(ctx, p, true)
				return pre(err)
			},
			Handle: s.restore, Authorize: s.authorizeRestore},
	}
}

func decodeBackup(p json.RawMessage) (StackBackupParams, error) {
	var v StackBackupParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	if !spec.ValidName(v.Name) {
		return v, badParam("not a stack name")
	}
	if v.Keep == 0 {
		v.Keep = backup.KeepDefault
	}
	if v.Keep < backup.KeepMin || v.Keep > backup.KeepMax {
		return v, badParam("keep is 1 to 30")
	}
	if len(v.Include) > backup.MaxPaths {
		return v, badParam("too many binds")
	}
	return v, nil
}

// volumeNames maps a spec's volume keys to Docker's names, sorted.
func volumeNames(sp spec.Spec) []string {
	out := make([]string, 0, len(sp.Volumes))
	for k, v := range sp.Volumes {
		n := sp.Name + "_" + k
		if v.Name != "" {
			n = v.Name
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type backupPlan struct {
	preview BackupPreview
	paths   []string
	spec    string // its SHA-256
}

// planBackup works out what a backup of v.Name holds, from the stored
// spec, the path check's sizes and the Engine's volumes. It writes nothing.
func (s *stacks) planBackup(ctx context.Context, v StackBackupParams) (backupPlan, error) {
	sp, raw, sum, err := s.stored(v.Name)
	if err != nil {
		return backupPlan{}, err
	}
	m, err := s.c.Measure(ctx, raw)
	if err != nil {
		return backupPlan{}, Errorf(CodeFailed, "%s", err.Error())
	}
	if !m.OK {
		return backupPlan{}, badParam("refused: %s", strings.Join(m.Refused, "; "))
	}
	pv := BackupPreview{Name: v.Name, Volumes: []BackupVolume{}, Binds: []backup.Bind{}, Secrets: []string{},
		Keep: v.Keep, Archives: []string{}, Prune: []string{}}
	pl := backupPlan{paths: []string{backup.StackFolder(v.Name)}, spec: sum}
	pv.Estimate = 1 << 20 // the stack's own files, generously

	names := volumeNames(sp)
	info := map[string]VolumeInfo{}
	if len(names) > 0 {
		if info, err = s.c.Volumes.BackupVolumes(ctx, names); err != nil {
			return backupPlan{}, err
		}
	}
	for _, n := range names {
		in := info[n]
		bv := BackupVolume{Name: n, Size: in.Size}
		switch {
		case !in.Exists:
			bv.Why = "it does not exist yet"
		case !in.Plain:
			bv.Why = "not a plain local volume, so Hearth does not back it up"
		case in.Size < 0:
			bv.Why = "its size is unknown"
		default:
			bv.Included = true
			pv.Estimate += in.Size
			pl.paths = append(pl.paths, backup.VolumeData(n))
		}
		pv.Volumes = append(pv.Volumes, bv)
	}

	include := map[string]bool{}
	for _, r := range v.Include {
		include[r] = true
	}
	seen := map[string]bool{}
	for _, b := range m.Binds {
		if seen[b.Real] {
			continue
		}
		seen[b.Real] = true
		sz := m.Sizes[b.Real]
		bb := backup.Bind{Service: b.Service, Source: b.Source, Real: b.Real, Size: sz.Bytes, Partial: sz.Partial,
			OtherFS: b.Dev != m.BackupDev}
		bb.Included = !bb.OtherFS || include[b.Real]
		delete(include, b.Real)
		pv.Binds = append(pv.Binds, bb)
	}
	if len(include) > 0 {
		return backupPlan{}, badParam("include names a path that is not one of the app's binds")
	}
	// A bind inside another included bind is in the archive already.
	sort.Slice(pv.Binds, func(i, j int) bool { return pv.Binds[i].Real < pv.Binds[j].Real })
	var taken []string
	for _, b := range pv.Binds {
		if !b.Included {
			continue
		}
		nested := slices.ContainsFunc(taken, func(t string) bool { return strings.HasPrefix(b.Real, t+"/") })
		if !nested {
			taken = append(taken, b.Real)
			pl.paths = append(pl.paths, b.Real)
			pv.Estimate += b.Size
		}
		pv.Partial = pv.Partial || b.Partial
	}
	if len(pl.paths) > backup.MaxPaths {
		return backupPlan{}, badParam("the app has more than 64 folders to back up")
	}

	for _, sec := range s.secretList(v.Name, sp) {
		if sec.Set {
			pv.Secrets = append(pv.Secrets, sec.Key)
		}
	}
	list, err := s.c.Engine.StackContainers(ctx, v.Name)
	if err != nil {
		return backupPlan{}, err
	}
	pv.Running = slices.ContainsFunc(list, func(c StackContainer) bool { return c.State == "running" })

	free, err := s.c.FreeSpace(filepath.Dir(s.backupsDir()))
	if err != nil {
		return backupPlan{}, Errorf(CodeFailed, "the free space for backups could not be read")
	}
	pv.Free = int64(min(free, 1<<62)) // #nosec G115 -- bounded
	pv.Enough = pv.Free >= pv.Estimate+freeMargin
	pv.Archives = s.archives(v.Name)
	// One more archive is about to be added.
	if p := backup.Prune(append(slices.Clone(pv.Archives), "99999999T999999Z.tar.zst"), v.Keep); p != nil {
		pv.Prune = p
	}
	pl.preview = pv
	return pl, nil
}

// archives lists the archive names of a stack, oldest first.
func (s *stacks) archives(name string) []string {
	entries, err := os.ReadDir(s.backupsDir() + "/" + name)
	out := []string{}
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.Type().IsRegular() && backup.ValidArchive(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// backupFolder opens (making, 0700) the stack's backup folder as a root.
func (s *stacks) backupFolder(name string) (*os.Root, error) {
	if err := os.MkdirAll(s.backupsDir(), 0o700); err != nil {
		return nil, err
	}
	top, err := os.OpenRoot(s.backupsDir())
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	if err := top.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	fi, err := top.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("the backup folder is not a folder")
	}
	if fi.Mode().Perm() != 0o700 {
		if err := top.Chmod(name, 0o700); err != nil {
			return nil, err
		}
	}
	return top.OpenRoot(name)
}

func (s *stacks) backup(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, err := decodeBackup(p)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(v.Name)()
	pl, err := s.planBackup(ctx, v)
	if err != nil {
		return nil, err
	}
	if !pl.preview.Enough {
		return nil, Errorf(CodeRefused, "not enough free space for the backup")
	}
	dir, err := s.backupFolder(v.Name)
	if err != nil {
		return nil, Errorf(CodeFailed, "the backup folder could not be made")
	}
	defer func() { _ = dir.Close() }()
	now := s.c.Now()
	stamp := backup.Stamp(now)
	if _, err := dir.Lstat(backup.ArchiveName(stamp)); err == nil {
		return nil, Errorf(CodeConflict, "a backup was made this second; try again")
	}
	plan := backup.Plan{Stack: v.Name, Output: backup.PartialName(stamp), Paths: pl.paths}
	if err := plan.Check(); err != nil {
		return nil, Errorf(CodeRefused, "%s", err.Error())
	}
	b, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(dir, backup.PlanFile, b); err != nil {
		return nil, Errorf(CodeFailed, "the backup plan could not be written")
	}
	defer func() { _ = dir.Remove(backup.PlanFile) }()

	stopped := false
	if v.StopFirst && pl.preview.Running {
		if _, err := s.run(ctx, v.Name, "stop"); err != nil {
			return nil, err
		}
		stopped = true
	}
	runErr := s.c.Run(ctx, BackupUnit(v.Name))
	var startErr error
	if stopped {
		_, startErr = s.run(context.WithoutCancel(ctx), v.Name, "start")
	}
	changed := backup.ChangedName(plan.Output)
	warning := ""
	if _, err := dir.Lstat(changed); err == nil {
		warning = "files changed while they were read; the archive may be inconsistent"
		_ = dir.Remove(changed)
	}
	if runErr != nil {
		_ = dir.Remove(plan.Output)
		return nil, Errorf(CodeFailed, "the backup failed (journal of %s)%s", BackupUnit(v.Name), startedAgain(stopped, startErr))
	}
	sum, size, err := backup.FileSHA256(s.backupRoots(), v.Name, plan.Output)
	if err != nil {
		_ = dir.Remove(plan.Output)
		return nil, Errorf(CodeFailed, "the archive could not be read back%s", startedAgain(stopped, startErr))
	}
	_, raw, _, _ := s.stored(v.Name)
	vols := []backup.Volume{}
	for _, bv := range pl.preview.Volumes {
		if bv.Included {
			vols = append(vols, backup.Volume{Name: bv.Name, Size: bv.Size})
		}
	}
	m := backup.Manifest{Version: 1, Stack: v.Name, Archive: backup.ArchiveName(stamp), Created: now.UTC().Format(time.RFC3339),
		SHA256: sum, Size: size, StoppedFirst: stopped, Keep: v.Keep, SpecSHA256: pl.spec, Spec: raw,
		Volumes: vols, Binds: pl.preview.Binds, Secrets: pl.preview.Secrets, Warning: warning}
	mb, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(dir, backup.ManifestName(stamp), mb); err != nil {
		_ = dir.Remove(plan.Output)
		return nil, Errorf(CodeFailed, "the manifest could not be written%s", startedAgain(stopped, startErr))
	}
	if err := dir.Rename(plan.Output, m.Archive); err != nil {
		_ = dir.Remove(backup.ManifestName(stamp))
		_ = dir.Remove(plan.Output)
		return nil, Errorf(CodeFailed, "the archive could not be named%s", startedAgain(stopped, startErr))
	}
	removed := []string{}
	for _, old := range backup.Prune(s.archives(v.Name), v.Keep) {
		if dir.Remove(old) == nil {
			removed = append(removed, old)
		}
		_ = dir.Remove(backup.ManifestOf(old))
	}
	if d, err := dir.Open("."); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if startErr != nil {
		return nil, Errorf(CodeFailed, "the backup %s was made, but the app did not start again: %v", m.Archive, startErr)
	}
	return BackupResult{Archive: m.Archive, Size: size, Warning: warning, Removed: removed}, nil
}

func startedAgain(stopped bool, err error) string {
	switch {
	case !stopped:
		return ""
	case err != nil:
		return "; the app did not start again"
	}
	return "; the app was started again"
}

// manifest reads and checks the manifest of archive, and that the archive
// is there with the size it records.
func (s *stacks) manifest(name, archive string) (backup.Manifest, error) {
	if !spec.ValidName(name) {
		return backup.Manifest{}, badParam("not a stack name")
	}
	if !backup.ValidArchive(archive) {
		return backup.Manifest{}, badParam("not an archive name")
	}
	r, err := os.OpenRoot(s.backupsDir() + "/" + name)
	if err != nil {
		return backup.Manifest{}, Errorf(CodeNotFound, "no such archive")
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Lstat(archive)
	if err != nil || !fi.Mode().IsRegular() {
		return backup.Manifest{}, Errorf(CodeNotFound, "no such archive")
	}
	f, err := r.Open(backup.ManifestOf(archive))
	if err != nil {
		return backup.Manifest{}, Errorf(CodeNotFound, "the archive has no manifest")
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, backup.MaxManifest+1)
	n, _ := readFull(f, b)
	m, err := backup.ParseManifest(b[:n], name, archive)
	if err != nil {
		return m, Errorf(CodeRefused, "%s", err.Error())
	}
	if m.Size != fi.Size() {
		return m, Errorf(CodeRefused, "the archive is not the size its manifest records")
	}
	return m, nil
}

func readFull(f *os.File, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		k, err := f.Read(b[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *stacks) backupList(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
	var v statusParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !spec.ValidName(v.Name) {
		return nil, badParam("not a stack name")
	}
	out := BackupList{Name: v.Name, Dir: backup.StackDir(v.Name), Archives: []BackupEntry{}}
	names := s.archives(v.Name)
	slices.Reverse(names)
	for _, a := range names {
		m, err := s.manifest(v.Name, a)
		if err != nil {
			continue // half written, or not Hearth's
		}
		out.Archives = append(out.Archives, BackupEntry{Archive: a, Created: m.Created, Size: m.Size, StoppedFirst: m.StoppedFirst,
			Keep: m.Keep, Volumes: m.Volumes, Binds: m.Binds, Secrets: m.Secrets, Warning: m.Warning})
	}
	return out, nil
}

type restoreCheck struct {
	manifest backup.Manifest
	spec     spec.Spec
	flags    []string
	current  string // the stored spec's SHA-256, "" when none
	paths    pathcheck.Reply
}

// checkArchive checks what restoring archive would put in place: the
// archived spec passes today's check, under the same name, and a Compose
// project outside Hearth does not hold the name.
func (s *stacks) checkArchive(ctx context.Context, name, archive string) (restoreCheck, error) {
	m, err := s.manifest(name, archive)
	if err != nil {
		return restoreCheck{}, err
	}
	sp, rep, err := spec.Parse(m.Spec)
	if err != nil {
		return restoreCheck{}, Errorf(CodeRefused, "the archived spec: %s", err.Error())
	}
	if !rep.OK() {
		return restoreCheck{}, Errorf(CodeRefused, "the archived spec is refused today: %s", strings.Join(rep.Refused, "; "))
	}
	if sp.Name != name {
		return restoreCheck{}, Errorf(CodeRefused, "the archived spec is another app's")
	}
	rc := restoreCheck{manifest: m, spec: sp, flags: rep.Flags}
	if rc.flags == nil {
		rc.flags = []string{}
	}
	if _, _, sum, err := s.stored(name); err == nil {
		rc.current = sum
	} else if be := (*Error)(nil); !errors.As(err, &be) || be.Code != CodeNotFound {
		return restoreCheck{}, err
	}
	if err := s.foreign(ctx, name); err != nil {
		return restoreCheck{}, err
	}
	return rc, nil
}

func (s *stacks) restorePreview(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	var v StackRestorePreviewParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	rc, err := s.checkArchive(ctx, v.Name, v.Archive)
	if err != nil {
		return nil, err
	}
	if r, err := s.c.Paths.Check(ctx, rc.manifest.Spec); err != nil {
		return nil, Errorf(CodeFailed, "%s", err.Error())
	} else if !r.OK {
		return nil, Errorf(CodeRefused, "the archived spec's binds: %s", strings.Join(r.Refused, "; "))
	}
	m := rc.manifest
	out := RestorePreview{Name: v.Name, Archive: v.Archive, Created: m.Created, Volumes: []RestoreVolume{},
		Secrets: m.Secrets, NewStack: rc.current == "", SpecChanged: rc.current != m.SpecSHA256, Flags: rc.flags,
		Binds: []backup.Bind{}, Warning: m.Warning}
	if out.Secrets == nil {
		out.Secrets = []string{}
	}
	for _, b := range m.Binds {
		if b.Included {
			out.Binds = append(out.Binds, b)
		}
	}
	names := make([]string, 0, len(m.Volumes))
	for _, vol := range m.Volumes {
		names = append(names, vol.Name)
	}
	info := map[string]VolumeInfo{}
	if len(names) > 0 {
		if info, err = s.c.Volumes.BackupVolumes(ctx, names); err != nil {
			return nil, err
		}
	}
	for _, vol := range m.Volumes {
		in := info[vol.Name]
		if in.Exists && !in.Plain {
			return nil, Errorf(CodeRefused, "%s is not a plain local volume any more", vol.Name)
		}
		cur := in.Size
		if !in.Exists {
			cur = 0
		}
		out.Volumes = append(out.Volumes, RestoreVolume{Name: vol.Name, Archived: vol.Size, Current: cur, Exists: in.Exists})
	}
	list, err := s.c.Engine.StackContainers(ctx, v.Name)
	if err != nil {
		return nil, err
	}
	out.Running = slices.ContainsFunc(list, func(c StackContainer) bool { return c.State == "running" })
	if out.Token, err = s.grants.put(restoreGrant{name: v.Name, archive: v.Archive, sha: m.SHA256, current: rc.current}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *stacks) checkRestore(ctx context.Context, p json.RawMessage, pre bool) (restoreCheck, error) {
	var v StackRestoreParams
	if err := decodeParams(p, &v); err != nil {
		return restoreCheck{}, err
	}
	if !spec.ValidName(v.Name) {
		return restoreCheck{}, badParam("not a stack name")
	}
	if v.TypedName != v.Name {
		return restoreCheck{}, badParam("type the app's name to restore it")
	}
	g, ok := s.grants.get(v.Token, !pre)
	if !ok || g.name != v.Name || g.archive != v.Archive {
		return restoreCheck{}, badParam("the restore preview has expired or is another; preview it again")
	}
	rc, err := s.checkArchive(ctx, v.Name, v.Archive)
	if err != nil {
		return rc, err
	}
	if rc.manifest.SHA256 != g.sha || rc.current != g.current {
		return rc, badParam("the archive or the app changed since the preview; preview it again")
	}
	ack := slices.Clone(v.AckFlags)
	slices.Sort(ack)
	if !slices.Equal(ack, rc.flags) {
		return rc, badParam("ack_flags must name exactly the archived spec's danger flags")
	}
	if rc.paths, err = s.paths(ctx, rc.manifest.Spec, pre); err != nil {
		return rc, err
	}
	return rc, nil
}

func (s *stacks) authorizeRestore(p json.RawMessage) (string, map[string]string) {
	var v StackRestoreParams
	if err := decodeParams(p, &v); err != nil {
		return ActionStackDanger, nil
	}
	m, err := s.manifest(v.Name, v.Archive)
	if err != nil {
		return ActionStackDanger, nil
	}
	sp, _, err := spec.Parse(m.Spec)
	if err != nil {
		return ActionStackDanger, nil
	}
	if a, d := dangerAction(sp, "restore"); a == ActionStackDanger {
		return a, d
	}
	return ActionStackRestore, map[string]string{
		"polkit.message": fmt.Sprintf("Hearth wants to restore the app %s from %s, overwriting its volumes", v.Name, v.Archive),
		"app":            v.Name,
	}
}

func (s *stacks) restore(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	var v StackRestoreParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !spec.ValidName(v.Name) {
		return nil, badParam("not a stack name")
	}
	defer s.locks.lock(v.Name)()
	rc, err := s.checkRestore(ctx, p, false)
	if err != nil {
		return nil, err
	}
	m := rc.manifest
	if rc.current != "" {
		if _, err := s.run(ctx, v.Name, "stop"); err != nil {
			return nil, err
		}
	}
	// The archived spec, checked above, with containers and volumes made
	// but nothing started.
	if _, err := s.c.Store.Prepare(m.Spec, "create"); err != nil {
		return nil, Errorf(CodeFailed, "the app's files could not be written; it is stopped")
	}
	if err := s.c.Run(ctx, StackUnit(v.Name)); err != nil {
		return nil, Errorf(CodeFailed, "its containers and volumes could not be made; it is stopped")
	}
	names := make([]string, 0, len(m.Volumes))
	for _, vol := range m.Volumes {
		names = append(names, vol.Name)
	}
	if len(names) > 0 {
		info, err := s.c.Volumes.BackupVolumes(ctx, names)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if in := info[n]; !in.Exists || !in.Plain {
				return nil, Errorf(CodeRefused, "%s is not a plain local volume; nothing was restored and the app is stopped", n)
			}
		}
	}
	dir, err := s.backupFolder(v.Name)
	if err != nil {
		return nil, Errorf(CodeFailed, "the backup folder could not be opened")
	}
	defer func() { _ = dir.Close() }()
	plan := backup.RestorePlan{Stack: v.Name, Archive: v.Archive, SHA256: m.SHA256, Volumes: names, Secrets: true}
	b, err := json.Marshal(plan)
	if err == nil {
		err = plan.Check()
	}
	if err == nil {
		err = writeAtomic(dir, backup.RestoreFile, b)
	}
	if err != nil {
		return nil, Errorf(CodeFailed, "the restore plan could not be written")
	}
	runErr := s.c.Run(ctx, RestoreUnit(v.Name))
	_ = dir.Remove(backup.RestoreFile)
	if runErr != nil {
		return nil, Errorf(CodeFailed, "the restore failed (journal of %s); the app is stopped and its volumes may be partly restored", RestoreUnit(v.Name))
	}
	res, err := s.runAndVerify(ctx, v.Name, "up", rc.paths)
	if err != nil {
		return nil, err
	}
	s.auditDanger(ctx, c, "stack.restore", rc.spec)
	return res, nil
}

// BackupVolumes inspects each named volume and asks the Engine for their
// sizes; a volume is Plain when it uses the local driver with no options
// and keeps its data where Hearth looks for it.
func (d *Docker) BackupVolumes(ctx context.Context, names []string) (map[string]VolumeInfo, error) {
	out := map[string]VolumeInfo{}
	var df struct {
		Volumes []struct {
			Name      string
			UsageData *struct{ Size int64 }
		}
	}
	if err := d.getInto(ctx, "/system/df", url.Values{"type": {"volume"}}, &df); err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, v := range df.Volumes {
		if v.UsageData != nil {
			sizes[v.Name] = v.UsageData.Size
		}
	}
	for _, n := range names {
		if !ValidVolume(n) {
			return nil, badParam("not a volume name")
		}
		var in struct {
			Driver     string
			Mountpoint string
			Options    map[string]string
		}
		if err := d.getInto(ctx, "/volumes/"+esc(n), nil, &in); err != nil {
			var be *Error
			if errors.As(err, &be) && be.Code == CodeNotFound {
				out[n] = VolumeInfo{Size: -1}
				continue
			}
			return nil, err
		}
		size, ok := sizes[n]
		if !ok {
			size = -1
		}
		out[n] = VolumeInfo{Exists: true, Size: size,
			Plain: in.Driver == "local" && len(in.Options) == 0 && in.Mountpoint == backup.VolumeData(n)}
	}
	return out, nil
}

// FreeSpace is what an unprivileged user could still write at path, as df
// shows it.
func FreeSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil // #nosec G115 -- block size is positive
}
