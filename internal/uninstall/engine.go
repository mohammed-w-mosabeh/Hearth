package uninstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
)

// The run (docs/plan/uninstall.md §3): one fixed order, every step
// audited and recorded, and the first failure stops it.

// Steps, in the order they run.
const (
	StepRevalidate = "revalidate"
	StepProcesses  = "processes"
	StepAuthorize  = "authorize"
	StepUnits      = "units"
	StepProgram    = "program"
	StepConffiles  = "conffiles"
	StepPurge      = "purge"
	StepOrphans    = "orphans"
	StepUser       = "user"
	StepSystem     = "system"
	StepRefs       = "refs"
)

// States of an operation.
const (
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateRestored = "restored"
)

// StepResult is one thing a step did, or tried.
type StepResult struct {
	Step   string `json:"step"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Done   bool   `json:"done"`
	Detail string `json:"detail,omitempty"`
}

// PackageVersion is a removed package, for reinstalling it.
type PackageVersion struct {
	ID      string `json:"id"` // name:arch
	Version string `json:"version"`
}

// ModeRef is a mode item an uninstall removed.
type ModeRef struct {
	Mode   string `json:"mode"`
	Kind   string `json:"kind"` // app, unit, or open
	Target string `json:"target"`
	Do     string `json:"do,omitempty"`
}

// Undo is what Restore needs.
type Undo struct {
	Packages  []PackageVersion  `json:"packages,omitempty"`
	Flatpaks  []string          `json:"flatpaks,omitempty"`
	Snaps     []string          `json:"snaps,omitempty"`
	Others    []string          `json:"others,omitempty"` // npm, cargo, Go: shown, reinstalled by the user
	Trashed   []Trashed         `json:"trashed,omitempty"`
	Archived  []string          `json:"archived,omitempty"` // paths the broker archived (moved)
	Favorites []string          `json:"favorites,omitempty"`
	Rules     []store.AlertRule `json:"rules,omitempty"`
	Playtime  []store.Playtime  `json:"playtime,omitempty"`
	Modes     []ModeRef         `json:"modes,omitempty"`
}

// Record is one operation.
type Record struct {
	ID       string       `json:"id"`
	Item     string       `json:"item"`
	Name     string       `json:"name"`
	Created  int64        `json:"created"`
	State    string       `json:"state"`
	Failed   string       `json:"failed,omitempty"` // the step that stopped the run
	BrokerOp string       `json:"broker_op,omitempty"`
	Rows     []Candidate  `json:"rows"` // what the user confirmed
	Steps    []StepResult `json:"steps"`
	Undo     Undo         `json:"undo"`
	Freed    int64        `json:"freed"`
}

// BrokerCaller calls hearth-broker (broker.Client).
type BrokerCaller interface {
	Call(ctx context.Context, method string, params, out any) error
}

// RefEditor removes Hearth's own references, returning what they were.
type RefEditor interface {
	RemoveFavorite(ctx context.Context, id string) error
	DeleteAlertRule(ctx context.Context, id int64) (store.AlertRule, error)
	TakePlaytime(ctx context.Context, id string) (store.Playtime, bool, error)
	RemoveModeRef(ctx context.Context, mode, kind, target string) (do string, err error)
}

// Engine runs plans.
type Engine struct {
	Save   func(ctx context.Context, r Record) error
	Broker BrokerCaller
	PK     PackageKit
	// Extra removes an npm, cargo, Go, Flatpak or snap package by its
	// listing's ID (pkgs.RemoveExtra).
	Extra func(ctx context.Context, kind, id string) error
	// Signal sends sig to pid if it still started at start; Alive says
	// whether it runs.
	Signal func(pid int, start uint64, sig syscall.Signal) error
	Alive  func(pid int, start uint64) bool
	// UserUnit stops and disables one of the user's units.
	UserUnit func(ctx context.Context, name string) error
	Trash    Trasher
	// Open opens a URL (Steam's own uninstall dialog).
	Open  func(ctx context.Context, url string) error
	Refs  RefEditor
	Audit func(ctx context.Context, action, target, result string)
	Now   func() time.Time
	// Grace is how long a process gets to quit before it is killed.
	Grace time.Duration
}

// ErrStopped wraps the error that stopped a run.
var ErrStopped = errors.New("the uninstall stopped")

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// run is one run's state.
type run struct {
	e   *Engine
	rec Record
}

func (r *run) save(ctx context.Context) {
	if r.e.Save != nil {
		_ = r.e.Save(ctx, r.rec)
	}
}

func (r *run) result(step string, c Candidate, err error) error {
	res := StepResult{Step: step, ID: c.ID, Name: c.Name, Done: err == nil}
	result := "ok"
	if err != nil {
		res.Detail, result = err.Error(), "failed: "+err.Error()
	}
	r.rec.Steps = append(r.rec.Steps, res)
	if r.e.Audit != nil {
		r.e.Audit(context.Background(), "uninstall."+step, c.ID, result)
	}
	return err
}

// Selected filters p's candidates by the keys sel; remains are never
// selected.
func Selected(p Plan, sel map[string]bool) []Candidate {
	var out []Candidate
	for _, c := range p.Candidates {
		if c.Group != GroupRemains && sel[c.Key()] {
			out = append(out, c)
		}
	}
	return out
}

func of(rows []Candidate, group string, kinds ...string) []Candidate {
	var out []Candidate
	for _, c := range rows {
		if c.Group == group && (len(kinds) == 0 || slices.Contains(kinds, c.Kind)) {
			out = append(out, c)
		}
	}
	return out
}

func ids(rows []Candidate) []string {
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		out = append(out, c.ID)
	}
	return out
}

// Run runs the rows of p the user selected, in the fixed order, and
// returns the record. A failure stops the run: the record says what was
// done, what failed and what never started.
func (e *Engine) Run(ctx context.Context, p Plan, rows []Candidate) (Record, error) {
	ctx = context.WithoutCancel(ctx) // a removal is not left halfway because the browser went away
	r := &run{e: e, rec: Record{ID: newID(), Item: p.Item, Name: p.Name, Created: e.now().UnixMilli(), State: StateRunning, Rows: rows}}
	for _, c := range rows {
		if c.Group != GroupPurge {
			r.rec.Freed += c.Size
		}
	}
	r.save(ctx)
	steps := []struct {
		name string
		fn   func(context.Context, *run, Plan, []Candidate) error
	}{
		{StepRevalidate, revalidate}, {StepProcesses, processes}, {StepAuthorize, authorize}, {StepUnits, units},
		{StepProgram, programStep}, {StepPurge, purgeStep}, {StepOrphans, orphanStep},
		{StepUser, userLeftovers}, {StepSystem, systemLeftovers}, {StepRefs, refs},
	}
	for _, s := range steps {
		if err := s.fn(ctx, r, p, rows); err != nil {
			r.rec.State, r.rec.Failed = StateFailed, s.name
			r.save(ctx)
			return r.rec, fmt.Errorf("%w at %s: %w", ErrStopped, s.name, err)
		}
		r.save(ctx)
	}
	r.rec.State = StateDone
	r.save(ctx)
	return r.rec, nil
}

// revalidate checks every path is still the file previewed before
// anything changes.
func revalidate(_ context.Context, r *run, p Plan, rows []Candidate) error {
	if p.Blocked != "" {
		return r.result(StepRevalidate, Candidate{ID: p.Item, Name: p.Name}, errors.New(p.Blocked))
	}
	for _, c := range rows {
		if c.Kind != KindTrash && c.Kind != KindArchive {
			continue
		}
		if err := Recheck(c); err != nil {
			return r.result(StepRevalidate, c, err)
		}
	}
	return nil
}

func processes(_ context.Context, r *run, _ Plan, rows []Candidate) error {
	grace := r.e.Grace
	if grace == 0 {
		grace = 10 * time.Second
	}
	for _, c := range of(rows, GroupRunning, KindProcess) {
		var pid int
		var start uint64
		if _, err := fmt.Sscanf(c.ID, "pid:%d:%d", &pid, &start); err != nil {
			return r.result(StepProcesses, c, errors.New("bad process"))
		}
		if !r.e.Alive(pid, start) {
			_ = r.result(StepProcesses, c, nil)
			continue
		}
		if err := r.e.Signal(pid, start, syscall.SIGTERM); err != nil && r.e.Alive(pid, start) {
			return r.result(StepProcesses, c, err)
		}
		deadline := r.e.now().Add(grace)
		for r.e.Alive(pid, start) && r.e.now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if r.e.Alive(pid, start) {
			_ = r.e.Signal(pid, start, syscall.SIGKILL)
			time.Sleep(200 * time.Millisecond)
		}
		if r.e.Alive(pid, start) {
			return r.result(StepProcesses, c, errors.New("it is still running"))
		}
		_ = r.result(StepProcesses, c, nil)
	}
	return nil
}

// brokerPlan is what the uninstall asks of the broker.
func brokerPlan(p Plan, rows []Candidate) broker.UninstallAuthorizeParams {
	v := broker.UninstallAuthorizeParams{Item: p.Name}
	for _, c := range of(rows, GroupRunning, KindUnit) {
		if name, ok := strings.CutPrefix(c.ID, "system/"); ok {
			v.Units = append(v.Units, name)
		}
	}
	for _, c := range of(rows, GroupPurge, KindPackage) {
		v.Purge = append(v.Purge, c.ID)
		v.Conffiles = append(v.Conffiles, c.ID)
	}
	for _, c := range rows {
		if c.Kind == KindArchive {
			v.Archive = append(v.Archive, archive.Move{Path: c.ID, Dev: c.Dev, Ino: c.Ino, Kind: archive.Kind(c.Type)})
		}
	}
	return v
}

func authorize(ctx context.Context, r *run, p Plan, rows []Candidate) error {
	v := brokerPlan(p, rows)
	if len(v.Units)+len(v.Purge)+len(v.Archive) == 0 {
		return nil
	}
	var resp struct {
		Op string `json:"op"`
	}
	if err := r.e.Broker.Call(ctx, "uninstall.authorize", v, &resp); err != nil {
		return r.result(StepAuthorize, Candidate{ID: p.Item, Name: p.Name}, err)
	}
	r.rec.BrokerOp = resp.Op
	return r.result(StepAuthorize, Candidate{ID: resp.Op, Name: p.Name}, nil)
}

func units(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	for _, c := range of(rows, GroupRunning, KindUnit) {
		var err error
		if name, ok := strings.CutPrefix(c.ID, "system/"); ok {
			err = r.e.Broker.Call(ctx, "unit.disable", broker.UnitDisableParams{Op: r.rec.BrokerOp, Unit: name}, nil)
		} else {
			err = r.e.UserUnit(ctx, strings.TrimPrefix(c.ID, "user/"))
		}
		if err := r.result(StepUnits, c, err); err != nil {
			return err
		}
	}
	return trashRows(ctx, r, StepUnits, of(rows, GroupRunning, KindTrash))
}

func trashRows(ctx context.Context, r *run, step string, rows []Candidate) error {
	if len(rows) == 0 {
		return nil
	}
	done, err := r.e.Trash.Trash(ctx, rows)
	r.rec.Undo.Trashed = append(r.rec.Undo.Trashed, done...)
	for _, c := range rows {
		if slices.ContainsFunc(done, func(t Trashed) bool { return t.Path == c.ID }) {
			_ = r.result(step, c, nil)
		}
	}
	if err != nil {
		for _, c := range rows {
			if !slices.ContainsFunc(done, func(t Trashed) bool { return t.Path == c.ID }) {
				return r.result(step, c, err)
			}
		}
		return err
	}
	return nil
}

// removePackages removes ids through PackageKit, checking first that the
// transaction removes nothing outside allowed.
func removePackages(ctx context.Context, r *run, step string, rows []Candidate, allowed []string) error {
	if len(rows) == 0 {
		return nil
	}
	pkIDs, err := resolveIDs(ctx, r.e.PK, ids(rows))
	if err != nil {
		return r.result(step, rows[0], err)
	}
	sim, err := r.e.PK.Simulate(ctx, pkgs.OpRemove, pkIDs)
	if err != nil {
		return r.result(step, rows[0], err)
	}
	if err := CheckTransaction(removedBy(sim), allowed); err != nil {
		return r.result(step, rows[0], err)
	}
	if err := r.e.PK.Apply(ctx, pkgs.OpRemove, pkIDs); err != nil {
		return r.result(step, rows[0], err)
	}
	for _, c := range sim {
		if c.Info == "removing" || c.Info == "obsoleting" {
			r.rec.Undo.Packages = append(r.rec.Undo.Packages, PackageVersion{ID: c.Name + ":" + c.Arch, Version: c.Version})
		}
	}
	for _, c := range rows {
		_ = r.result(step, c, nil)
	}
	return nil
}

func programStep(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	prog := of(rows, GroupProgram)
	if err := removePackages(ctx, r, StepProgram, of(prog, GroupProgram, KindPackage), ids(of(prog, GroupProgram, KindPackage))); err != nil {
		return err
	}
	for _, c := range prog {
		var err error
		switch c.Kind {
		case KindFlatpak, KindSnap:
			err = r.e.Extra(ctx, c.Kind, c.ID)
			if err == nil && c.Kind == KindFlatpak {
				r.rec.Undo.Flatpaks = append(r.rec.Undo.Flatpaks, c.ID)
			} else if err == nil {
				r.rec.Undo.Snaps = append(r.rec.Undo.Snaps, c.ID)
			}
		case KindExtra:
			kind, id, _ := strings.Cut(c.ID, ":")
			err = r.e.Extra(ctx, kind, id)
			if err == nil {
				r.rec.Undo.Others = append(r.rec.Undo.Others, c.ID)
			}
		case KindSteam:
			err = r.e.Open(ctx, "steam://uninstall/"+c.ID)
		default:
			continue
		}
		if err := r.result(StepProgram, c, err); err != nil {
			return err
		}
	}
	if err := trashRows(ctx, r, StepProgram, of(prog, GroupProgram, KindTrash)); err != nil {
		return err
	}
	return archiveRows(ctx, r, StepProgram, of(prog, GroupProgram, KindArchive))
}

func archiveRows(ctx context.Context, r *run, step string, rows []Candidate) error {
	if len(rows) == 0 {
		return nil
	}
	err := r.e.Broker.Call(ctx, "leftovers.archive", broker.ArchiveParams{Op: r.rec.BrokerOp, Paths: ids(rows)}, nil)
	for _, c := range rows {
		if err == nil {
			r.rec.Undo.Archived = append(r.rec.Undo.Archived, c.ID)
		}
		if rerr := r.result(step, c, err); rerr != nil {
			return rerr
		}
	}
	return nil
}

// purgeRows copies then purges the configuration of rows' packages, those
// that are in the purge group.
func purgeRows(ctx context.Context, r *run, step string, rows []Candidate, among []string) error {
	var names []string
	for _, c := range of(rows, GroupPurge, KindPackage) {
		if slices.Contains(among, c.ID) {
			names = append(names, c.ID)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if err := r.e.Broker.Call(ctx, "leftovers.archive", broker.ArchiveParams{Op: r.rec.BrokerOp, Conffiles: names}, nil); err != nil {
		return r.result(StepConffiles, Candidate{ID: strings.Join(names, " "), Name: "configuration files"}, err)
	}
	_ = r.result(StepConffiles, Candidate{ID: strings.Join(names, " "), Name: "configuration files"}, nil)
	var resp struct {
		Purged []string `json:"purged"`
		Left   []string `json:"left"`
	}
	err := r.e.Broker.Call(ctx, "pkg.purge", broker.PurgeParams{Op: r.rec.BrokerOp, Names: names}, &resp)
	if err == nil && len(resp.Left) > 0 {
		err = fmt.Errorf("dpkg left %s", strings.Join(resp.Left, ", "))
	}
	return r.result(step, Candidate{ID: strings.Join(names, " "), Name: "configuration of " + strings.Join(names, ", ")}, err)
}

// purgeStep copies, then purges, the configuration the program's packages
// left (the copy is recorded as StepConffiles); the orphans' come after
// their removal.
func purgeStep(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	var prog []string
	for _, c := range r.rec.Undo.Packages {
		prog = append(prog, c.ID)
	}
	return purgeRows(ctx, r, StepPurge, rows, prog)
}

func orphanStep(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	orph := of(rows, GroupOrphans, KindPackage)
	before := len(r.rec.Undo.Packages)
	if err := removePackages(ctx, r, StepOrphans, orph, ids(orph)); err != nil {
		return err
	}
	for _, c := range of(rows, GroupOrphans, KindFlatpak) {
		err := r.e.Extra(ctx, KindFlatpak, c.ID)
		if err == nil {
			r.rec.Undo.Flatpaks = append(r.rec.Undo.Flatpaks, c.ID)
		}
		if err := r.result(StepOrphans, c, err); err != nil {
			return err
		}
	}
	var removed []string
	for _, c := range r.rec.Undo.Packages[before:] {
		removed = append(removed, c.ID)
	}
	return purgeRows(ctx, r, StepOrphans, rows, removed)
}

func userLeftovers(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	return trashRows(ctx, r, StepUser, of(rows, GroupUser, KindTrash))
}

func systemLeftovers(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	return archiveRows(ctx, r, StepSystem, of(rows, GroupSystem, KindArchive))
}

func refs(ctx context.Context, r *run, _ Plan, rows []Candidate) error {
	for _, c := range of(rows, GroupRefs, KindRef) {
		kind, rest, _ := strings.Cut(c.ID, ":")
		var err error
		switch kind {
		case "favorite":
			if err = r.e.Refs.RemoveFavorite(ctx, rest); err == nil {
				r.rec.Undo.Favorites = append(r.rec.Undo.Favorites, rest)
			}
		case "alert":
			var id int64
			if id, err = strconv.ParseInt(rest, 10, 64); err == nil {
				var rule store.AlertRule
				if rule, err = r.e.Refs.DeleteAlertRule(ctx, id); err == nil {
					r.rec.Undo.Rules = append(r.rec.Undo.Rules, rule)
				}
			}
		case "playtime":
			var pt store.Playtime
			var ok bool
			if pt, ok, err = r.e.Refs.TakePlaytime(ctx, rest); err == nil && ok {
				r.rec.Undo.Playtime = append(r.rec.Undo.Playtime, pt)
			}
		case "mode":
			mode, kt, _ := strings.Cut(rest, ":")
			k, target, _ := strings.Cut(kt, ":")
			var do string
			if do, err = r.e.Refs.RemoveModeRef(ctx, mode, k, target); err == nil {
				r.rec.Undo.Modes = append(r.rec.Undo.Modes, ModeRef{Mode: mode, Kind: k, Target: target, Do: do})
			}
		default:
			err = errors.New("unknown reference")
		}
		if err := r.result(StepRefs, c, err); err != nil {
			return err
		}
	}
	return nil
}

// Marshal is the record as stored.
func (r Record) Marshal() (json.RawMessage, error) { return json.Marshal(r) }
