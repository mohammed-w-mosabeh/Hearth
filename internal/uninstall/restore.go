package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"
)

// Restore (docs/plan/uninstall.md §6, SECURITY S16l): within 7 days, in
// the reverse order of the run, and never over anything that exists.

// madisonRe is "name | version | source"; a version is Debian's shape only
// (digits first; letters, digits and .+~:- after), so it can never carry
// a ";" into a PackageKit ID.
var madisonRe = regexp.MustCompile(`^\s*([a-z0-9][a-z0-9+.-]{1,127})\s*\|\s*([0-9][A-Za-z0-9.+~:-]{0,127})\s*\|`)

// ParseMadison maps each package to the versions apt can install, in apt's
// order (newest first).
func ParseMadison(out string) map[string][]string {
	res := map[string][]string{}
	for i, l := range strings.Split(out, "\n") {
		if i > 20000 {
			break
		}
		m := madisonRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if !slices.Contains(res[m[1]], m[2]) {
			res[m[1]] = append(res[m[1]], m[2])
		}
	}
	return res
}

// upstream is a Debian version without its revision.
func upstream(v string) string {
	if i := strings.LastIndexByte(v, '-'); i > 0 {
		return v[:i]
	}
	return v
}

// PickVersion chooses what to reinstall: want itself if apt has it, else
// the newest with the same upstream version, else the newest. exact says
// whether it is want; ok is false when apt has none.
func PickVersion(want string, avail []string) (v string, exact, ok bool) {
	if len(avail) == 0 {
		return "", false, false
	}
	if slices.Contains(avail, want) {
		return want, true, true
	}
	for _, a := range avail {
		if upstream(a) == upstream(want) {
			return a, false, true
		}
	}
	return avail[0], false, true
}

// RefRestorer puts Hearth's references back.
type RefRestorer interface {
	AddFavorite(ctx context.Context, id string) error
	CreateAlertRule(ctx context.Context, r store.AlertRule) (int64, error)
	PutPlaytime(ctx context.Context, p store.Playtime) error
	AddModeRef(ctx context.Context, r ModeRef) error
}

// Restorer restores operations.
type Restorer struct {
	DB      *store.DB
	Madison func(ctx context.Context, names ...string) (string, error)
	PK      PackageKit
	Broker  BrokerCaller
	// Run runs a fixed command as the user (gio trash --restore, flatpak
	// install).
	Run   RunFunc
	Home  string
	Snapd func(ctx context.Context, method, path string, body any, out any) error
	Refs  RefRestorer
	Audit func(ctx context.Context, action, target, result string)
	Now   func() time.Time
}

// RestoreRow is one thing a restore brings back, or cannot.
type RestoreRow struct {
	Kind   string `json:"kind"` // ref, archived, package, flatpak, snap, conffile, trash, manual
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok"`               // it can be restored
	Done   bool   `json:"done"`             // after the run
	Pkg    string `json:"pkg_id,omitempty"` // the PackageKit ID to install
}

type restoreState struct {
	rec  Record
	rows []RestoreRow
}

func (r *Restorer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Restorer) load(ctx context.Context, id string) (Record, error) {
	var rec Record
	if !rules.ValidOp(id) {
		return rec, actions.Errorf(http.StatusBadRequest, "target must be an operation ID")
	}
	op, ok, err := r.DB.GetUninstallOp(ctx, id)
	if err != nil {
		return rec, actions.Errorf(http.StatusServiceUnavailable, "the history could not be read")
	}
	if !ok {
		return rec, actions.Errorf(http.StatusNotFound, "no such uninstall")
	}
	if err := json.Unmarshal(op.Record, &rec); err != nil {
		return rec, actions.Errorf(http.StatusConflict, "the record could not be read")
	}
	if r.now().Sub(time.UnixMilli(rec.Created)) > KeepOps {
		return rec, actions.Errorf(http.StatusGone, "this uninstall is older than 7 days")
	}
	if rec.State == StateRestored || rec.State == StateRunning {
		return rec, actions.Errorf(http.StatusConflict, "this uninstall is %s", rec.State)
	}
	return rec, nil
}

// inTrash reports whether a trashed path is still in the home trash.
func (r *Restorer) inTrash(t Trashed) bool {
	if t.Name == "" || strings.ContainsAny(t.Name, "/\x00") {
		return false
	}
	_, err := os.Lstat(filepath.Join(r.Home, ".local/share/Trash/info", t.Name+".trashinfo"))
	return err == nil
}

// rowsFor lists what restoring rec would bring back.
func (r *Restorer) rowsFor(ctx context.Context, rec Record) []RestoreRow {
	var rows []RestoreRow
	u := rec.Undo
	for _, f := range u.Favorites {
		rows = append(rows, RestoreRow{Kind: "ref", ID: "favorite:" + f, Name: f, Detail: "a favorite again", OK: true})
	}
	for _, rule := range u.Rules {
		rows = append(rows, RestoreRow{Kind: "ref", ID: fmt.Sprintf("alert:%d", rule.ID), Name: rule.Name, Detail: "the alert rule", OK: true})
	}
	for _, p := range u.Playtime {
		rows = append(rows, RestoreRow{Kind: "ref", ID: "playtime:" + p.ID, Name: p.ID, Detail: "the tracked playtime", OK: true})
	}
	for _, m := range u.Modes {
		rows = append(rows, RestoreRow{Kind: "ref", ID: modeRef(m.Mode, m.Kind, m.Target), Name: m.Mode + ": " + m.Target,
			Detail: "back in the mode file; its triggers stay disarmed until you arm them", OK: true})
	}
	for _, p := range u.Archived {
		_, err := os.Lstat(p)
		row := RestoreRow{Kind: "archived", ID: p, Name: p, OK: err != nil, Detail: "from the archive"}
		if err == nil {
			row.Detail = "something is there again; it stays as it is"
		}
		rows = append(rows, row)
	}
	var names []string
	for _, p := range u.Packages {
		n, _, _ := strings.Cut(p.ID, ":")
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	avail := map[string][]string{}
	if len(names) > 0 && r.Madison != nil {
		if out, err := r.Madison(ctx, names...); err == nil {
			avail = ParseMadison(out)
		}
	}
	for _, p := range u.Packages {
		n, arch, _ := strings.Cut(p.ID, ":")
		v, exact, ok := PickVersion(p.Version, avail[n])
		row := RestoreRow{Kind: "package", ID: p.ID, Name: n, OK: ok}
		switch {
		case !ok:
			row.Detail = "apt no longer has it"
		case exact:
			row.Detail = "version " + v
		default:
			row.Detail = "version differs: " + p.Version + " → " + v
		}
		row.Pkg = n + ";" + v + ";" + arch + ";"
		rows = append(rows, row)
	}
	for _, f := range u.Flatpaks {
		rows = append(rows, RestoreRow{Kind: "flatpak", ID: f, Name: f, Detail: "installed again from its remote", OK: true})
	}
	for _, s := range u.Snaps {
		rows = append(rows, RestoreRow{Kind: "snap", ID: s, Name: s, Detail: "installed again, then snapd's snapshot of its data restored", OK: true})
	}
	for _, o := range u.Others {
		rows = append(rows, RestoreRow{Kind: "manual", ID: o, Name: o, Detail: "install it again yourself"})
	}
	if rec.BrokerOp != "" && r.Broker != nil {
		var ops []broker.LeftoverOp
		if err := r.Broker.Call(ctx, "leftovers.list", nil, &ops); err == nil {
			for _, op := range ops {
				if op.Op != rec.BrokerOp {
					continue
				}
				for _, e := range op.Entries {
					if e.Kind == rules.EntryConffile {
						rows = append(rows, RestoreRow{Kind: "conffile", ID: e.Path, Name: e.Path, OK: true,
							Detail: "your version, over the package's default"})
					}
				}
			}
		}
	}
	for _, t := range u.Trashed {
		row := RestoreRow{Kind: "trash", ID: t.Path, Name: t.Path, OK: t.URI != "" && r.inTrash(t), Detail: "from the trash"}
		switch {
		case t.URI == "":
			row.Detail = "in another disk's trash: restore it from there"
		case !row.OK:
			row.Detail = "no longer in the trash"
		}
		if _, err := os.Lstat(t.Path); err == nil && row.OK {
			row.OK, row.Detail = false, "something is there again; it stays as it is"
		}
		rows = append(rows, row)
	}
	return rows
}

func (r *Restorer) plan(ctx context.Context, req actions.Request) (actions.Plan, error) {
	out := actions.Plan{Summary: "restore", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	rec, err := r.load(ctx, req.Target)
	if err != nil {
		return out, err
	}
	out.Summary = "restore " + rec.Name
	rows := r.rowsFor(ctx, rec)
	for _, row := range rows {
		it := actions.Item{ID: row.Kind + "|" + row.ID, Name: row.Name, Detail: row.Detail}
		if row.OK {
			out.Items = append(out.Items, it)
		} else {
			out.Blocked = append(out.Blocked, actions.Blocked{Item: it, Reason: "cannot"})
		}
	}
	out.State = restoreState{rec: rec, rows: rows}
	return out, nil
}

func (r *Restorer) step(ctx context.Context, row *RestoreRow, err error) {
	row.Done = err == nil
	result := "ok"
	if err != nil {
		row.Detail, result = err.Error(), "failed: "+err.Error()
	}
	if r.Audit != nil {
		r.Audit(ctx, "uninstall.restore."+row.Kind, row.ID, result)
	}
}

func (r *Restorer) run(ctx context.Context, _ actions.Request, ap actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	st, ok := ap.State.(restoreState)
	if !ok {
		return res, nil
	}
	ctx = context.WithoutCancel(ctx)
	rows := st.rows
	idx := func(kind string) []int {
		var out []int
		for i, row := range rows {
			if row.Kind == kind && row.OK {
				out = append(out, i)
			}
		}
		return out
	}
	// 1. References.
	u := st.rec.Undo
	for _, i := range idx("ref") {
		kind, rest, _ := strings.Cut(rows[i].ID, ":")
		var err error
		switch kind {
		case "favorite":
			err = r.Refs.AddFavorite(ctx, rest)
		case "alert":
			for _, rule := range u.Rules {
				if fmt.Sprintf("alert:%d", rule.ID) == rows[i].ID {
					_, err = r.Refs.CreateAlertRule(ctx, rule)
				}
			}
		case "playtime":
			for _, p := range u.Playtime {
				if p.ID == rest {
					err = r.Refs.PutPlaytime(ctx, p)
				}
			}
		case "mode":
			for _, m := range u.Modes {
				if modeRef(m.Mode, m.Kind, m.Target) == rows[i].ID {
					err = r.Refs.AddModeRef(ctx, m)
				}
			}
		}
		r.step(ctx, &rows[i], err)
	}
	// 2. Archived system leftovers.
	if ids := idx("archived"); len(ids) > 0 {
		var paths []string
		for _, i := range ids {
			paths = append(paths, rows[i].ID)
		}
		r.restoreArchive(ctx, st.rec.BrokerOp, rows, ids, paths)
	}
	// 3. Packages: apt, then Flatpak and snaps.
	if ids := idx("package"); len(ids) > 0 {
		r.installPackages(ctx, rows, ids)
	}
	for _, i := range idx("flatpak") {
		f := strings.Split(rows[i].ID, "/")
		var err error
		if len(f) != 5 || (f[0] != "system" && f[0] != "user") {
			err = errors.New("bad ref")
		} else {
			_, err = r.Run(ctx, "Reinstall "+f[2], "/usr/bin/flatpak",
				[]string{"flatpak", "install", "-y", "--noninteractive", "--" + f[0], strings.Join(f[1:], "/")}, []string{"HOME=" + r.Home, "PATH=/usr/bin:/bin"})
		}
		r.step(ctx, &rows[i], err)
	}
	for _, i := range idx("snap") {
		r.step(ctx, &rows[i], r.restoreSnap(ctx, rows[i].ID))
	}
	// 4. Configuration files, over what the reinstalled packages put back.
	if ids := idx("conffile"); len(ids) > 0 {
		var paths []string
		for _, i := range ids {
			paths = append(paths, rows[i].ID)
		}
		r.restoreArchive(ctx, st.rec.BrokerOp, rows, ids, paths)
	}
	// 5. The user's files, from the trash, never over anything.
	for _, i := range idx("trash") {
		for _, t := range u.Trashed {
			if t.Path == rows[i].ID {
				r.step(ctx, &rows[i], r.untrash(ctx, t))
			}
		}
	}
	rec := st.rec
	all := true
	for _, row := range rows {
		if row.OK && row.Done {
			res.Done++
		} else if row.OK {
			all = false
			res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: row.Kind + "|" + row.ID, Name: row.Name, Detail: row.Detail}, Reason: row.Kind})
		}
	}
	if all {
		rec.State = StateRestored
	}
	raw, _ := json.Marshal(struct {
		Record
		Restore []RestoreRow `json:"restore"`
	}{rec, rows})
	_ = r.DB.SaveUninstallOp(ctx, store.UninstallOp{ID: rec.ID, Item: rec.Item, Name: rec.Name, Created: rec.Created,
		Updated: r.now().UnixMilli(), State: rec.State, Record: raw})
	return res, nil
}

func (r *Restorer) restoreArchive(ctx context.Context, op string, rows []RestoreRow, ids []int, paths []string) {
	var resp struct {
		Results []struct {
			Path   string `json:"path"`
			Done   bool   `json:"done"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	err := r.Broker.Call(ctx, "leftovers.restore", broker.RestoreLeftoversParams{Op: op, Paths: paths}, &resp)
	for _, i := range ids {
		e := err
		if e == nil {
			e = errors.New("the broker did not say")
			for _, x := range resp.Results {
				if x.Path == rows[i].ID {
					e = nil
					if !x.Done {
						e = errors.New(x.Reason)
					}
				}
			}
		}
		r.step(ctx, &rows[i], e)
	}
}

// installPackages installs the chosen versions through PackageKit, after a
// simulation; a version PackageKit cannot install falls back to apt's
// candidate, and says so.
func (r *Restorer) installPackages(ctx context.Context, rows []RestoreRow, ids []int) {
	var pk []string
	for _, i := range ids {
		pk = append(pk, rows[i].Pkg)
	}
	if _, err := r.PK.Simulate(ctx, pkgs.OpInstall, pk); err != nil {
		// Fall back to the candidates apt picks now.
		var names []string
		for _, i := range ids {
			names = append(names, rows[i].Name)
		}
		found, rerr := r.PK.Resolve(ctx, false, names)
		if rerr != nil {
			for _, i := range ids {
				r.step(ctx, &rows[i], fmt.Errorf("PackageKit: %w", err))
			}
			return
		}
		pk = pk[:0]
		for _, i := range ids {
			for _, c := range found {
				if c.Name == rows[i].Name && strings.HasSuffix(rows[i].ID, ":"+c.Arch) {
					pk = append(pk, c.ID)
					rows[i].Detail = "version now apt's: " + c.Version
				}
			}
		}
	}
	err := r.PK.Apply(ctx, pkgs.OpInstall, pk)
	for _, i := range ids {
		r.step(ctx, &rows[i], err)
	}
}

// untrash puts a trashed path back with local paths only: gio trash
// --restore needs gvfs for its trash:// URI, which a session may lack.
// gio move never overwrites, so something at the place again stays.
func (r *Restorer) untrash(ctx context.Context, t Trashed) error {
	if t.Name == "" || strings.ContainsAny(t.Name, "/\x00") || !filepath.IsAbs(t.Path) || filepath.Clean(t.Path) != t.Path {
		return errors.New("not restorable")
	}
	trash := filepath.Join(r.Home, ".local/share/Trash")
	file, info := filepath.Join(trash, "files", t.Name), filepath.Join(trash, "info", t.Name+".trashinfo")
	if _, err := os.Lstat(t.Path); err == nil {
		return errors.New("something is there again; it stays as it is")
	}
	if dir := filepath.Dir(t.Path); dir != "/" {
		if _, err := os.Stat(dir); err != nil {
			if _, err := r.Run(ctx, "Restore from the trash", "/usr/bin/gio", []string{"gio", "mkdir", "-p", "--", dir}, nil); err != nil {
				return err
			}
		}
	}
	if _, err := r.Run(ctx, "Restore from the trash", "/usr/bin/gio", []string{"gio", "move", "--", file, t.Path}, nil); err != nil {
		return err
	}
	_, _ = r.Run(ctx, "Restore from the trash", "/usr/bin/gio", []string{"gio", "remove", "--", info}, nil)
	return nil
}

// restoreSnap installs a snap again and restores snapd's newest automatic
// snapshot of it.
func (r *Restorer) restoreSnap(ctx context.Context, name string) error {
	if r.Snapd == nil {
		return errors.New("snapd is not available")
	}
	var ch struct {
		Change string `json:"change"`
	}
	if err := r.Snapd(ctx, http.MethodPost, "/v2/snaps/"+url.PathEscape(name), map[string]string{"action": "install"}, &ch); err != nil {
		return err
	}
	var sets []struct {
		ID        int `json:"id"`
		Snapshots []struct {
			Snap string `json:"snap"`
		} `json:"snapshots"`
	}
	if err := r.Snapd(ctx, http.MethodGet, "/v2/snapshots?snaps="+url.QueryEscape(name), nil, &sets); err != nil || len(sets) == 0 {
		return errors.New("installed again, but snapd has no snapshot of its data")
	}
	newest := 0
	for _, set := range sets {
		newest = max(newest, set.ID)
	}
	return r.Snapd(ctx, http.MethodPost, "/v2/snapshots", map[string]any{"action": "restore", "set": newest, "snaps": []string{name}}, nil)
}

// Register adds uninstall/restore.
func (r *Restorer) Register(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "uninstall", Name: "restore", Risk: actions.Destructive,
		Validate: func(req actions.Request) error {
			if !rules.ValidOp(req.Target) {
				return errors.New("target must be an operation ID")
			}
			if len(req.Params) != 0 {
				return errors.New("params must be empty")
			}
			return nil
		},
		Plan: r.plan,
		Run:  r.run,
	})
}

// AddFavorite puts id back among the favorites.
func (d DBRefs) AddFavorite(ctx context.Context, id string) error {
	f, err := d.favorites(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(f.IDs, id) {
		f.IDs = append(f.IDs, id)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return d.DB.SetSetting(ctx, favoritesKey, raw)
}

// CreateAlertRule creates the rule again (it gets a new ID).
func (d DBRefs) CreateAlertRule(ctx context.Context, rule store.AlertRule) (int64, error) {
	rule.ID = 0
	return d.DB.CreateAlertRule(ctx, rule)
}

// PutPlaytime puts a game's tracked time back.
func (d DBRefs) PutPlaytime(ctx context.Context, p store.Playtime) error {
	return d.DB.PutPlaytime(ctx, p)
}

// AddModeRef puts a mode item or app to open back into its file.
func (d DBRefs) AddModeRef(ctx context.Context, m ModeRef) error {
	if d.Modes == nil {
		return errors.New("modes are not available")
	}
	return d.Modes.Edit(ctx, m.Mode, func(md *modes.Mode) bool {
		if m.Kind == "open" {
			if slices.Contains(md.Session.Open, m.Target) {
				return false
			}
			md.Session.Open = append(md.Session.Open, m.Target)
			return true
		}
		for _, it := range md.Items {
			if it.Kind == m.Kind && it.Target == m.Target {
				return false
			}
		}
		md.Items = append(md.Items, modes.Item{Kind: m.Kind, Target: m.Target, Do: m.Do})
		return true
	})
}
