package space

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

// files/trash (ROADMAP 12.5, SECURITY S16c): the matching files of one
// folder in the home go to the trash, at most 16 a run. Only what
// space/trash's "large" target would take qualifies: the user's own
// regular files, directly in the folder, outside hidden folders, reached
// through no link; each is checked again by device and inode when run.

// FilesKind is the action's kind: POST /api/v1/actions/files/trash.
const FilesKind = "files"

// MaxDays bounds the "older than" parameter.
const MaxDays = 3650

var (
	// folderRe is a folder under the home, written "~/a/b".
	folderRe = regexp.MustCompile(`^~/[^/\x00-\x1f\x7f][^\x00-\x1f\x7f]{0,199}$`)
	// globRe is a file name pattern: no separator, no class or escape
	// (filepath.Match's "[" and "\"), so it reads as typed.
	globRe = regexp.MustCompile(`^[^/\\\[\]\x00-\x1f\x7f]{1,64}$`)
)

// checkFiles validates files/trash's target and params.
func checkFiles(r actions.Request) error {
	if !folderRe.MatchString(r.Target) {
		return errors.New(`target must be a folder in your home, written "~/folder"`)
	}
	rel := strings.TrimPrefix(r.Target, "~/")
	if filepath.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return errors.New("target must be a clean path inside your home")
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return errors.New("target must not be a hidden folder")
		}
	}
	for k := range r.Params {
		if k != "pattern" && k != "days" {
			return fmt.Errorf("unknown param %q", k)
		}
	}
	if !globRe.MatchString(r.Params["pattern"]) {
		return errors.New("pattern must be a file name pattern such as *.iso")
	}
	if _, err := filepath.Match(r.Params["pattern"], ""); err != nil {
		return errors.New("pattern is not valid")
	}
	if d, ok := r.Params["days"]; ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 || n > MaxDays || strconv.Itoa(n) != d {
			return fmt.Errorf("days must be 0 to %d", MaxDays)
		}
	}
	return nil
}

// filesFolder is the target's folder, which must be reached through no
// link and be a folder.
func (a *Advisor) filesFolder(target string) (string, error) {
	dir := filepath.Join(a.cfg.Home, strings.TrimPrefix(target, "~/"))
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.New("the folder does not exist")
	}
	if real != dir {
		return "", errors.New("the folder is reached through a link")
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", errors.New("not a folder")
	}
	return dir, nil
}

func (a *Advisor) planFiles(ctx context.Context, r actions.Request) (actions.Plan, error) {
	p := actions.Plan{Summary: "move matching files of " + r.Target + " to the trash", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	if _, err := os.Stat(a.cfg.Gio); err != nil {
		return p, actions.Errorf(http.StatusServiceUnavailable, "gio is not installed (package libglib2.0-bin)")
	}
	dir, err := a.filesFolder(r.Target)
	if err != nil {
		p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: r.Target, Name: r.Target, Detail: err.Error()}, Reason: ReasonRefused})
		return p, nil
	}
	days, _ := strconv.Atoi(r.Params["days"])
	cut := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p, actions.Errorf(http.StatusInternalServerError, "the folder could not be read")
	}
	type found struct {
		pk  picked
		mod time.Time
	}
	var all []found
	for i, e := range entries {
		if i >= maxScanFiles || ctx.Err() != nil {
			break
		}
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if ok, _ := filepath.Match(r.Params["pattern"], e.Name()); !ok {
			continue
		}
		pk, fi, err := a.check("large", filepath.Join(dir, e.Name()))
		if err != nil || !fi.ModTime().Before(cut) {
			continue
		}
		all = append(all, found{pk: pk, mod: fi.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.Before(all[j].mod) }) // oldest first
	var state []picked
	for i, f := range all {
		name := filepath.Base(f.pk.path)
		if i >= maxSelected {
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: f.pk.path, Name: name,
				Detail: fmt.Sprintf("more than %d files: left for a later run", maxSelected)}, Reason: ReasonRefused})
			continue
		}
		size, _ := measure(ctx, f.pk.path)
		p.Items = append(p.Items, actions.Item{ID: f.pk.path, Name: name, Detail: human(size)})
		state = append(state, f.pk)
	}
	p.State = state
	return p, nil
}

// maxScanFiles bounds the entries of the folder looked at.
const maxScanFiles = 20000

// RegisterFiles adds files/trash and space/preview. files/trash is destructive: previewed and
// confirmed, and in an automation it waits for approval (S15d).
func (a *Advisor) RegisterFiles(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: FilesKind, Name: "trash", Risk: actions.Destructive, Authority: actions.AuthorityNone,
		Validate: checkFiles, Plan: a.planFiles,
		Run: func(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
			res, err := a.runTrash(ctx, actions.Request{Target: "large"}, plan)
			res.Blocked = plan.Blocked
			return res, err
		}})
	// space/preview changes nothing: a run measures and reports; the
	// cleanup itself is chosen on the Space page.
	reg.Register(actions.Action{Kind: "space", Name: "preview", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			if r.Target != "space" || len(r.Params) != 0 {
				return errors.New(`target must be "space" and params empty`)
			}
			return nil
		},
		Plan: a.planPreview,
		Run: func(_ context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			return actions.Result{Done: len(p.Items), Failed: []actions.Blocked{}}, nil
		}})
}

// planPreview measures what could be freed, as the Space page shows it.
func (a *Advisor) planPreview(ctx context.Context, _ actions.Request) (actions.Plan, error) {
	rep := a.Report(ctx)
	p := actions.Plan{Summary: "measure what could be freed (the Space page)", Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	add := func(id, name string, size int64) {
		if size > 0 {
			p.Items = append(p.Items, actions.Item{ID: id, Name: name, Detail: human(size)})
		}
	}
	add("apt", "Package cache", rep.Apt.Size)
	add("journal", "Journal", rep.Journal.Size)
	add("cache", "Cache folder", rep.Cache.Size)
	add("trash", "Trash", rep.Trash.Size)
	var kernels int64
	for _, k := range rep.Kernels {
		kernels += k.Size
	}
	add("kernels", "Old kernels", kernels)
	return p, nil
}
