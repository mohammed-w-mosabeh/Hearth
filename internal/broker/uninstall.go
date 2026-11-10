package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"
)

// Uninstall completely, the broker's part (SECURITY S16k, S16l): one
// polkit check authorizes a whole plan, which the broker keeps root-only
// for 30 minutes; each later call is checked against it and runs once.

const (
	// ActionUninstall authorizes one uninstall's broker steps:
	// auth_admin, no keep.
	ActionUninstall = "org.hearth.packages.uninstall"
	// ActionUninstallRestore authorizes putting archived files back.
	ActionUninstallRestore = "org.hearth.packages.restore"
	// UninstallPlanTTL is how long an authorized plan may be used.
	UninstallPlanTTL = 30 * time.Minute
	// EtcUnits holds the administrator's own units.
	EtcUnits = "/etc/systemd/system"

	maxUninstallUnits = 16
	maxUninstallPurge = 64
	maxItemRunes      = 80
	planFile          = "plan.json"
	purgeArgs         = "purge.args"
)

// UninstallAuthorizeParams are what one uninstall may do as root.
type UninstallAuthorizeParams struct {
	Item      string         `json:"item"`                // shown in the dialog
	Units     []string       `json:"units,omitempty"`     // system units to stop and disable
	Purge     []string       `json:"purge,omitempty"`     // packages to purge once only their configuration is left
	Archive   []archive.Move `json:"archive,omitempty"`   // leftovers to move into the archive
	Conffiles []string       `json:"conffiles,omitempty"` // packages whose configuration files are copied first
}

// UninstallPlan is an authorized plan, as the broker keeps it.
type UninstallPlan struct {
	UninstallAuthorizeParams
	Op      string `json:"op"`
	UID     uint32 `json:"uid"`
	Expires int64  `json:"expires"` // Unix ms
}

// UninstallOpParams name an operation.
type UninstallOpParams struct {
	Op string `json:"op"`
}

// UnitDisableParams are unit.disable's params.
type UnitDisableParams struct {
	Op   string `json:"op"`
	Unit string `json:"unit"`
}

// PurgeParams are pkg.purge's params.
type PurgeParams struct {
	Op    string   `json:"op"`
	Names []string `json:"names"`
}

// ArchiveParams are leftovers.archive's params: a subset of the plan.
type ArchiveParams struct {
	Op        string   `json:"op"`
	Paths     []string `json:"paths,omitempty"`
	Conffiles []string `json:"conffiles,omitempty"`
}

// RestoreLeftoversParams are leftovers.restore's params.
type RestoreLeftoversParams struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
}

// UninstallConfig configures UninstallMethods.
type UninstallConfig struct {
	Bus      func(ctx context.Context) (SystemBus, error)
	Roots    archive.Roots
	EtcUnits string // default EtcUnits
	Now      func() time.Time
	Poll     time.Duration
	// System replaces "/" for leftovers' paths (tests only; as Roots.System).
	System string
}

// criticalUnits are never stopped by an uninstall, whatever owns them.
var criticalUnits = []string{"dbus.service", "dbus.socket", "polkit.service", "systemd-", "user@", "getty@", "init.scope"}

func criticalUnit(u string) bool {
	if strings.HasPrefix(u, "hearth") {
		return true
	}
	for _, c := range criticalUnits {
		if u == c || strings.HasPrefix(u, c) {
			return true
		}
	}
	return false
}

// stoppableKind: the unit kinds an uninstall stops and disables.
func stoppableKind(n string) bool {
	for _, k := range []string{".service", ".socket", ".timer", ".path"} {
		if strings.HasSuffix(n, k) {
			return true
		}
	}
	return false
}

// cleanItem is the item's name as the dialog shows it.
func cleanItem(s string) (string, error) {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxItemRunes {
		return "", badParam("item must be a name of 1 to %d characters", maxItemRunes)
	}
	return s, nil
}

func distinct[T comparable](vs []T) bool {
	seen := make(map[T]bool, len(vs))
	for _, v := range vs {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// execStart is the program of a unit file's first ExecStart=.
func execStart(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(l), "ExecStart=")
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		f := strings.Fields(v)
		return strings.TrimLeft(f[0], "@-:+!|")
	}
	return ""
}

type uninstaller struct {
	c  UninstallConfig
	mu sync.Mutex // one purge at a time: one args file per operation
}

func (u *uninstaller) etc() string { return or(u.c.EtcUnits, EtcUnits) }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (u *uninstaller) sys(p string) string {
	if u.c.System == "" {
		return p
	}
	return filepath.Join(u.c.System, p)
}

// checkAuthorize validates an authorize call fully, reading only: shapes,
// then what is on disk now. Every refusal is bad params, the one answer
// the server gives before polkit, so nothing that would be refused raises
// a password dialog; and no refusal repeats the caller's strings, which
// the audit records.
func (u *uninstaller) checkAuthorize(p json.RawMessage) (UninstallAuthorizeParams, error) {
	var v UninstallAuthorizeParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	item, err := cleanItem(v.Item)
	if err != nil {
		return v, err
	}
	v.Item = item
	if len(v.Units)+len(v.Purge)+len(v.Archive) == 0 {
		return v, badParam("nothing to authorize")
	}
	if len(v.Units) > maxUninstallUnits || len(v.Purge) > maxUninstallPurge || len(v.Archive) > rules.MaxMoved ||
		len(v.Conffiles) > maxUninstallPurge {
		return v, badParam("too many entries")
	}
	if !distinct(v.Units) || !distinct(v.Purge) || !distinct(v.Archive) || !distinct(v.Conffiles) {
		return v, badParam("an entry is named twice")
	}
	for i, n := range v.Purge {
		if !rules.ValidPackage(n) {
			return v, badParam("purge entry %d is not a package name", i)
		}
	}
	for _, n := range v.Conffiles {
		if !slices.Contains(v.Purge, n) {
			return v, badParam("configuration files are copied only for packages being purged")
		}
	}
	paths := make([]string, 0, len(v.Archive))
	for i, a := range v.Archive {
		if _, _, err := rules.CheckSystem(a.Path); err != nil {
			return v, badParam("archive entry %d is not a leftover an uninstall may archive", i)
		}
		paths = append(paths, a.Path)
	}
	for i, n := range v.Units {
		if safeexec.UnitName(n) != nil || strings.Contains(n, "@.") || criticalUnit(n) || !stoppableKind(n) {
			return v, badParam("unit entry %d is not a unit an uninstall may stop", i)
		}
	}
	// What is on disk now.
	if len(v.Purge) > 0 {
		pkgs, err := archive.ReadStatus(u.c.Roots)
		if err != nil {
			return v, badParam("dpkg's status could not be read")
		}
		for i, n := range v.Purge {
			found := archive.Find(pkgs, n)
			if len(found) == 0 {
				return v, badParam("purge entry %d is not known to dpkg", i)
			}
			for _, p := range found {
				if why := p.Undeletable(); why != "" {
					return v, badParam("purge entry %d can never be removed (%s)", i, why)
				}
			}
		}
	}
	for i, a := range v.Archive {
		fi, err := os.Lstat(u.sys(a.Path))
		if err != nil {
			return v, badParam("archive entry %d is not there", i)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Dev != a.Dev || st.Ino != a.Ino {
			return v, badParam("archive entry %d changed since the preview", i)
		}
		if archive.SafeParent(u.sys(filepath.Dir(a.Path))) != nil {
			return v, badParam("archive entry %d is in a folder someone other than root can write", i)
		}
	}
	owned, err := archive.OwnedUnder(u.c.Roots, paths)
	if err != nil {
		return v, badParam("dpkg's file lists could not be read")
	}
	for i, p := range paths {
		if owned[p] != "" {
			return v, badParam("archive entry %d belongs to an installed package", i)
		}
	}
	for i, n := range v.Units {
		if !u.unitBelongs(n, v.Purge, paths) {
			return v, badParam("unit entry %d is not a unit of what is being removed", i)
		}
	}
	return v, nil
}

// unitBelongs: a unit file one of the packages ships, or an unowned unit
// in /etc/systemd/system whose program lies inside a leftover being
// archived.
func (u *uninstaller) unitBelongs(name string, purge, archived []string) bool {
	for _, pkg := range purge {
		for _, f := range archive.PackageFiles(u.c.Roots, pkg) {
			if f == "/usr/lib/systemd/system/"+name || f == "/lib/systemd/system/"+name {
				return true
			}
		}
	}
	p := filepath.Join(u.etc(), name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return false
	}
	b, err := os.ReadFile(p) // #nosec G304 -- a validated unit name in /etc/systemd/system
	if err != nil {
		return false
	}
	prog := execStart(b)
	for _, a := range archived {
		if prog == a || strings.HasPrefix(prog, a+"/") {
			owned, err := archive.OwnedUnder(u.c.Roots, []string{filepath.Join(EtcUnits, name)})
			return err == nil && owned[filepath.Join(EtcUnits, name)] == ""
		}
	}
	return false
}

func newOp() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (u *uninstaller) now() time.Time {
	if u.c.Now != nil {
		return u.c.Now()
	}
	return time.Now()
}

func (u *uninstaller) authorize(_ context.Context, c Caller, p json.RawMessage) (any, error) {
	v, err := u.checkAuthorize(p)
	if err != nil {
		return nil, err
	}
	op, err := newOp()
	if err != nil {
		return nil, Errorf(CodeFailed, "no operation ID")
	}
	if err := os.MkdirAll(filepath.Dir(u.c.Roots.OpDir(op)), 0o700); err != nil {
		return nil, Errorf(CodeFailed, "the plan could not be kept")
	}
	if err := os.Mkdir(u.c.Roots.OpDir(op), 0o700); err != nil {
		return nil, Errorf(CodeFailed, "the plan could not be kept")
	}
	plan := UninstallPlan{UninstallAuthorizeParams: v, Op: op, UID: c.UID, Expires: u.now().Add(UninstallPlanTTL).UnixMilli()}
	b, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err := writeExcl(filepath.Join(u.c.Roots.OpDir(op), planFile), b); err != nil {
		return nil, Errorf(CodeFailed, "the plan could not be kept")
	}
	return map[string]any{"op": op, "expires": plan.Expires}, nil
}

// writeExcl writes a new file, refusing anything already there.
func writeExcl(p string, b []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed name in a 0700 operation folder
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	return errors.Join(werr, cerr)
}

// plan loads op's plan for uid, while it is valid.
func (u *uninstaller) plan(op string, uid uint32) (UninstallPlan, error) {
	var v UninstallPlan
	if !rules.ValidOp(op) {
		return v, badParam("op must be an operation ID")
	}
	b, err := os.ReadFile(filepath.Join(u.c.Roots.OpDir(op), planFile)) // #nosec G304 -- a validated ID in the root-only folder
	if err != nil {
		return v, Errorf(CodeUnauthorized, "no such authorized uninstall")
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Op != op {
		return v, Errorf(CodeUnauthorized, "no such authorized uninstall")
	}
	if v.UID != uid {
		return v, Errorf(CodeUnauthorized, "the uninstall was authorized for another user")
	}
	if u.now().UnixMilli() >= v.Expires {
		return v, Errorf(CodeUnauthorized, "the authorization has expired; preview again")
	}
	return v, nil
}

// once marks a step of op done, failing if it already was: every step
// runs at most once.
func (u *uninstaller) once(op, step string) error {
	sum := sha256.Sum256([]byte(step))
	if err := writeExcl(filepath.Join(u.c.Roots.OpDir(op), "done-"+hex.EncodeToString(sum[:])), nil); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Errorf(CodeConflict, "%s was already done in this uninstall", step)
		}
		return Errorf(CodeFailed, "the step could not be recorded")
	}
	return nil
}

func (u *uninstaller) unitDisable(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	var v UnitDisableParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	plan, err := u.plan(v.Op, c.UID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(plan.Units, v.Unit) {
		return nil, Errorf(CodeUnauthorized, "%s is not in the authorized uninstall", v.Unit)
	}
	if err := u.once(v.Op, "unit "+v.Unit); err != nil {
		return nil, err
	}
	bus, err := u.c.Bus(ctx)
	if err != nil {
		return nil, Errorf(CodeFailed, "the system bus is not available")
	}
	defer func() { _ = bus.Close() }()
	var job dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, v.Unit, "replace"); err != nil {
		return nil, Errorf(CodeFailed, "%s could not be stopped: %v", v.Unit, err)
	}
	var unit dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".GetUnit", []any{&unit}, v.Unit); err == nil {
		poll := u.c.Poll
		if poll == 0 {
			poll = 500 * time.Millisecond
		}
		deadline := u.now().Add(2 * time.Minute)
		for u.now().Before(deadline) {
			props, err := bus.GetAll(ctx, sdDest, unit, sdUnit)
			if err != nil {
				break
			}
			if s, _ := props["ActiveState"].Value().(string); s == "inactive" || s == "failed" {
				break
			}
			select {
			case <-ctx.Done():
				return nil, Errorf(CodeFailed, "%s took too long to stop", v.Unit)
			case <-time.After(poll):
			}
		}
	}
	var changes []struct{ Type, File, Dest string }
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".DisableUnitFiles", []any{&changes}, []string{v.Unit}, false); err != nil {
		return nil, Errorf(CodeFailed, "%s was stopped but could not be disabled: %v", v.Unit, err)
	}
	_ = bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil)
	return map[string]any{"unit": v.Unit, "stopped": true, "disabled": true}, nil
}

func (u *uninstaller) purge(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	var v PurgeParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	plan, err := u.plan(v.Op, c.UID)
	if err != nil {
		return nil, err
	}
	if len(v.Names) == 0 || len(v.Names) > maxUninstallPurge || !distinct(v.Names) {
		return nil, badParam("names must be 1 to %d distinct packages", maxUninstallPurge)
	}
	pkgs, err := archive.ReadStatus(u.c.Roots)
	if err != nil {
		return nil, Errorf(CodeFailed, "dpkg's status could not be read")
	}
	var names, gone []string
	for _, n := range v.Names {
		if !slices.Contains(plan.Purge, n) {
			return nil, Errorf(CodeUnauthorized, "%s is not in the authorized uninstall", n)
		}
		found := archive.Find(pkgs, n)
		if len(found) == 0 {
			// Removed with no configuration files: dpkg forgot it, so
			// there is nothing left to purge.
			gone = append(gone, n)
			continue
		}
		if len(found) != 1 || !found[0].ConfigOnly() {
			return nil, Errorf(CodeConflict, "%s is not a package with only its configuration left", n)
		}
		if why := found[0].Undeletable(); why != "" {
			return nil, Errorf(CodeRefused, "%s can never be removed (%s)", n, why)
		}
		names = append(names, n)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, n := range v.Names {
		if err := u.once(v.Op, "purge "+n); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 {
		return map[string]any{"purged": gone, "left": []string{}}, nil
	}
	args := filepath.Join(u.c.Roots.OpDir(v.Op), purgeArgs)
	_ = os.Remove(args)
	if err := writeExcl(args, []byte("PURGE_ARGS="+strings.Join(names, " ")+"\n")); err != nil {
		return nil, Errorf(CodeFailed, "the purge could not be prepared")
	}
	defer func() { _ = os.Remove(args) }()
	runErr := runOneshot(ctx, u.c.Bus, u.c.Poll, "purge", "hearth-purge@"+v.Op+".service")
	after, err := archive.ReadStatus(u.c.Roots)
	if err != nil {
		return nil, Errorf(CodeFailed, "dpkg's status could not be read after the purge")
	}
	purged, left := gone, []string{}
	for _, n := range names {
		if len(archive.Find(after, n)) == 0 {
			purged = append(purged, n)
		} else {
			left = append(left, n)
		}
	}
	if runErr != nil && len(purged) == 0 {
		return nil, runErr
	}
	return map[string]any{"purged": purged, "left": left}, nil
}

func (u *uninstaller) archive(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	var v ArchiveParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	plan, err := u.plan(v.Op, c.UID)
	if err != nil {
		return nil, err
	}
	if len(v.Paths)+len(v.Conffiles) == 0 || !distinct(v.Paths) || !distinct(v.Conffiles) {
		return nil, badParam("name the paths or packages to archive, each once")
	}
	job := archive.Job{Op: v.Op, UID: c.UID, Item: plan.Item}
	for _, path := range v.Paths {
		i := slices.IndexFunc(plan.Archive, func(m archive.Move) bool { return m.Path == path })
		if i < 0 {
			return nil, Errorf(CodeUnauthorized, "%s is not in the authorized uninstall", path)
		}
		job.Moves = append(job.Moves, plan.Archive[i])
	}
	if len(v.Conffiles) > 0 {
		pkgs, err := archive.ReadStatus(u.c.Roots)
		if err != nil {
			return nil, Errorf(CodeFailed, "dpkg's status could not be read")
		}
		for _, n := range v.Conffiles {
			if !slices.Contains(plan.Conffiles, n) {
				return nil, Errorf(CodeUnauthorized, "%s is not in the authorized uninstall", n)
			}
			for _, pk := range archive.Find(pkgs, n) {
				for _, f := range pk.Conffiles {
					if rules.CheckConffile(f) == nil && len(job.Conffiles) < rules.MaxEntries-rules.MaxMoved {
						job.Conffiles = append(job.Conffiles, archive.Conffile{Package: pk.ID(), Path: f})
					}
				}
			}
		}
	}
	for _, m := range job.Moves {
		if err := u.once(v.Op, "archive "+m.Path); err != nil {
			return nil, err
		}
	}
	for _, n := range v.Conffiles {
		if err := u.once(v.Op, "conffiles "+n); err != nil {
			return nil, err
		}
	}
	if len(job.Moves)+len(job.Conffiles) == 0 {
		return map[string]any{"entries": []rules.Entry{}}, nil
	}
	if err := archive.WriteJob(u.c.Roots, job); err != nil {
		return nil, Errorf(CodeFailed, "the archive could not be prepared")
	}
	runErr := runOneshot(ctx, u.c.Bus, u.c.Poll, "archive", "hearth-archive@"+v.Op+".service")
	if runErr != nil {
		// What did reach the archive is in its manifest (leftovers.list).
		return nil, runErr
	}
	m, _, err := archive.ReadManifest(u.c.Roots, v.Op)
	if err != nil {
		return nil, Errorf(CodeFailed, "the archive's manifest could not be read")
	}
	return map[string]any{"entries": m.Entries}, nil
}

func (u *uninstaller) checkRestore(p json.RawMessage) (RestoreLeftoversParams, rules.Manifest, error) {
	var v RestoreLeftoversParams
	if err := decodeParams(p, &v); err != nil {
		return v, rules.Manifest{}, err
	}
	if !rules.ValidOp(v.Op) {
		return v, rules.Manifest{}, badParam("op must be an operation ID")
	}
	if len(v.Paths) == 0 || len(v.Paths) > rules.MaxEntries || !distinct(v.Paths) {
		return v, rules.Manifest{}, badParam("paths must be 1 to %d distinct paths", rules.MaxEntries)
	}
	m, ok, err := archive.ReadManifest(u.c.Roots, v.Op)
	if err != nil || !ok {
		return v, rules.Manifest{}, badParam("no archive of that operation can be restored")
	}
	for i, path := range v.Paths {
		if !slices.ContainsFunc(m.Entries, func(e rules.Entry) bool { return e.Path == path }) {
			return v, m, badParam("path %d is not in that archive", i)
		}
	}
	return v, m, nil
}

func (u *uninstaller) restore(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	v, m, err := u.checkRestore(p)
	if err != nil {
		return nil, err
	}
	if c.UID != 0 && m.UID != c.UID {
		return nil, Errorf(CodeUnauthorized, "that uninstall was another user's")
	}
	if err := os.MkdirAll(u.c.Roots.OpDir(v.Op), 0o700); err != nil {
		return nil, Errorf(CodeFailed, "the restore could not be prepared")
	}
	if err := archive.WriteRestore(u.c.Roots, archive.RestoreJob{Op: v.Op, Paths: v.Paths}); err != nil {
		return nil, Errorf(CodeFailed, "the restore could not be prepared")
	}
	runErr := runOneshot(ctx, u.c.Bus, u.c.Poll, "restore", "hearth-restore-leftovers@"+v.Op+".service")
	res, err := archive.ReadResults(u.c.Roots, v.Op)
	if err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, Errorf(CodeFailed, "the restore's results could not be read")
	}
	return map[string]any{"results": res}, nil
}

// LeftoverOp is one archived uninstall, as leftovers.list shows it.
type LeftoverOp struct {
	Op      string        `json:"op"`
	Item    string        `json:"item"`
	Created int64         `json:"created"`
	Entries []rules.Entry `json:"entries"`
}

// UninstallMethods are the broker's uninstall methods.
func UninstallMethods(c UninstallConfig) []Method {
	u := &uninstaller{c: c}
	inPlan := func(check func(UninstallPlan, json.RawMessage) bool) func(context.Context, Caller, json.RawMessage) (bool, bool) {
		// Decided by the authorized plan alone: polkit is never asked
		// again for a step the plan holds, and a step it does not hold is
		// refused.
		return func(_ context.Context, cl Caller, p json.RawMessage) (bool, bool) {
			var op UninstallOpParams
			if json.Unmarshal(p, &op) != nil {
				return true, false
			}
			plan, err := u.plan(op.Op, cl.UID)
			return true, err == nil && check(plan, p)
		}
	}
	shape := func(v any) func(context.Context, json.RawMessage) error {
		return func(_ context.Context, p json.RawMessage) error { return decodeParams(p, v) }
	}
	return []Method{{
		Name: "uninstall.authorize", Privileged: true, Action: ActionUninstall, Timeout: 5 * time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := u.checkAuthorize(p); return err },
		Authorize: func(p json.RawMessage) (string, map[string]string) {
			var v UninstallAuthorizeParams
			_ = json.Unmarshal(p, &v)
			item, _ := cleanItem(v.Item)
			msg := fmt.Sprintf("Uninstall %s: stop %d services, purge %d packages, archive %d system folders or files",
				item, len(v.Units), len(v.Purge), len(v.Archive))
			return "", map[string]string{"polkit.message": msg}
		},
		Handle: u.authorize,
	}, {
		Name: "unit.disable", Privileged: true, Action: ActionUninstall, Timeout: 3 * time.Minute,
		Check: shape(&UnitDisableParams{}),
		Authorized: inPlan(func(plan UninstallPlan, p json.RawMessage) bool {
			var v UnitDisableParams
			return decodeParams(p, &v) == nil && slices.Contains(plan.Units, v.Unit)
		}),
		Handle: u.unitDisable,
	}, {
		Name: "pkg.purge", Privileged: true, Action: ActionUninstall, Timeout: 15 * time.Minute,
		Check: shape(&PurgeParams{}),
		Authorized: inPlan(func(plan UninstallPlan, p json.RawMessage) bool {
			var v PurgeParams
			if decodeParams(p, &v) != nil || len(v.Names) == 0 {
				return false
			}
			for _, n := range v.Names {
				if !slices.Contains(plan.Purge, n) {
					return false
				}
			}
			return true
		}),
		Handle: u.purge,
	}, {
		Name: "leftovers.archive", Privileged: true, Action: ActionUninstall, Timeout: 6 * time.Hour,
		Check: shape(&ArchiveParams{}),
		Authorized: inPlan(func(plan UninstallPlan, p json.RawMessage) bool {
			var v ArchiveParams
			if decodeParams(p, &v) != nil {
				return false
			}
			for _, path := range v.Paths {
				if !slices.ContainsFunc(plan.Archive, func(m archive.Move) bool { return m.Path == path }) {
					return false
				}
			}
			for _, n := range v.Conffiles {
				if !slices.Contains(plan.Conffiles, n) {
					return false
				}
			}
			return true
		}),
		Handle: u.archive,
	}, {
		Name: "leftovers.list",
		Handle: func(_ context.Context, c Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			out := []LeftoverOp{}
			for _, m := range archive.ListManifests(u.c.Roots, c.UID) {
				out = append(out, LeftoverOp{Op: m.Op, Item: m.Item, Created: m.Created, Entries: m.Entries})
			}
			return out, nil
		},
	}, {
		Name: "leftovers.restore", Privileged: true, Action: ActionUninstallRestore, Timeout: 6 * time.Hour,
		Check:  func(_ context.Context, p json.RawMessage) error { _, _, err := u.checkRestore(p); return err },
		Handle: u.restore,
	}}
}
