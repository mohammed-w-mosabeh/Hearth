package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/backup"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Backups (ROADMAP 9.10, SECURITY S12d): stack/backup and stack/restore
// behind the actions endpoint, and the list and size preview routes. The
// broker plans and checks everything again; what is shown here is what it
// was asked.

type backupState struct{ Params broker.StackBackupParams }

type restoreState struct{ Params broker.StackRestoreParams }

// registerBackups adds stack/backup and stack/restore. A backup is a
// preview-then-confirm action because it may stop the app and removes the
// oldest archives past the number kept, which the preview lists; a restore
// overwrites the app's volumes, so its name is typed back too.
func registerBackups(reg *actions.Registry, b Broker) {
	reg.Register(actions.Action{Kind: Kind, Name: "backup", Risk: actions.Destructive,
		Validate: func(r actions.Request) error { _, err := backupParams(r); return err },
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			v, err := backupParams(r)
			if err != nil {
				return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%s", err.Error())
			}
			return planBackup(ctx, b, v)
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(backupState)
			if !ok {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.backup", st.Params)
		}})
	reg.Register(actions.Action{Kind: Kind, Name: "restore", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if !spec.ValidName(r.Target) {
				return errors.New("target must be a stack name")
			}
			for k := range r.Params {
				if k != "archive" {
					return fmt.Errorf("unknown param %q", k)
				}
			}
			if !backup.ValidArchive(r.Params["archive"]) {
				return errors.New("params.archive must be an archive's name")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planRestore(ctx, b, r.Target, r.Params["archive"])
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, ok := p.State.(restoreState)
			if !ok {
				return actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}, nil
			}
			return call(ctx, b, p, "stack.restore", st.Params)
		}})
}

// backupParams reads stop ("true" or "false"), keep (1 to 30) and include
// (a JSON list of bind paths on another filesystem).
func backupParams(r actions.Request) (broker.StackBackupParams, error) {
	v := broker.StackBackupParams{Name: r.Target, Keep: backup.KeepDefault, StopFirst: true, Include: []string{}}
	if !spec.ValidName(r.Target) {
		return v, errors.New("target must be a stack name")
	}
	for k, val := range r.Params {
		switch k {
		case "stop":
			if val != "true" && val != "false" {
				return v, errors.New(`params.stop is "true" or "false"`)
			}
			v.StopFirst = val == "true"
		case "keep":
			n, err := strconv.Atoi(val)
			if err != nil || n < backup.KeepMin || n > backup.KeepMax {
				return v, errors.New("params.keep is 1 to 30")
			}
			v.Keep = n
		case "include":
			if len(val) > 64<<10 || json.Unmarshal([]byte(val), &v.Include) != nil || len(v.Include) > backup.MaxPaths {
				return v, errors.New("params.include is a JSON list of at most 64 paths")
			}
			if v.Include == nil {
				v.Include = []string{}
			}
		default:
			return v, fmt.Errorf("unknown param %q", k)
		}
	}
	return v, nil
}

func previewBackup(ctx context.Context, b Broker, v broker.StackBackupParams) (broker.BackupPreview, error) {
	var pv broker.BackupPreview
	err := b.Call(ctx, "stack.backup.preview", broker.StackBackupParams{Name: v.Name, Keep: v.Keep, Include: v.Include}, &pv)
	return pv, err
}

// Size writes a byte count for a plan's text.
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func planBackup(ctx context.Context, b Broker, v broker.StackBackupParams) (actions.Plan, error) {
	p := actions.Plan{Summary: "back up " + v.Name, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	pv, err := previewBackup(ctx, b, v)
	if err != nil {
		var be *broker.Error
		switch {
		case errors.As(err, &be) && be.Code == broker.CodeNotFound:
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: v.Name, Name: v.Name}, Reason: ReasonNotFound})
			return p, nil
		case errors.As(err, &be) && (be.Code == broker.CodeBadParams || be.Code == broker.CodeRefused):
			p.Blocked = append(p.Blocked, blocked(v.Name, be.Message))
			return p, nil
		}
		return p, brokerError(err)
	}
	for _, vol := range pv.Volumes {
		it := actions.Item{ID: "volume:" + vol.Name, Name: vol.Name, Detail: "volume, " + Size(vol.Size)}
		if !vol.Included {
			it.Detail = "volume, left out: " + vol.Why
		}
		p.Items = append(p.Items, it)
	}
	for _, bind := range pv.Binds {
		it := actions.Item{ID: "bind:" + bind.Real, Name: bind.Real, Detail: "bind folder, " + Size(bind.Size)}
		if bind.Partial {
			it.Detail = "bind folder, at least " + Size(bind.Size)
		}
		if !bind.Included {
			it.Detail = "bind folder on another filesystem, left out"
		}
		p.Items = append(p.Items, it)
	}
	if len(pv.Secrets) > 0 {
		p.Items = append(p.Items, actions.Item{ID: "secrets", Name: "secrets.env",
			Detail: "the app's secrets, by name: " + strings.Join(pv.Secrets, ", ")})
	}
	for _, old := range pv.Prune {
		p.Items = append(p.Items, actions.Item{ID: "prune:" + old, Name: old, Detail: "an older backup, removed to keep " + strconv.Itoa(v.Keep)})
	}
	if v.StopFirst && pv.Running {
		p.Items = append(p.Items, actions.Item{ID: "stop", Name: v.Name, Detail: "stopped during the backup, then started again"})
	}
	est := Size(pv.Estimate)
	if pv.Partial {
		est = "at least " + est
	}
	p.Summary = fmt.Sprintf("back up %s: %s before compression, %s free", v.Name, est, Size(pv.Free))
	if !pv.Enough {
		p.Blocked = append(p.Blocked, blocked(v.Name, "not enough free space in /var/lib/hearth/backups"))
		return p, nil
	}
	v.Include = includedOther(pv)
	p.State = backupState{Params: v}
	return p, nil
}

// includedOther is what the preview included from other filesystems: the
// run asks for exactly that.
func includedOther(pv broker.BackupPreview) []string {
	out := []string{}
	for _, bind := range pv.Binds {
		if bind.OtherFS && bind.Included {
			out = append(out, bind.Real)
		}
	}
	return out
}

func planRestore(ctx context.Context, b Broker, name, archive string) (actions.Plan, error) {
	p := actions.Plan{Summary: "restore " + name + " from " + archive, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	var pv broker.RestorePreview
	if err := b.Call(ctx, "stack.restore.preview", broker.StackRestorePreviewParams{Name: name, Archive: archive}, &pv); err != nil {
		var be *broker.Error
		switch {
		case errors.As(err, &be) && be.Code == broker.CodeNotFound:
			p.Blocked = append(p.Blocked, blocked(name, be.Message))
			return p, nil
		case errors.As(err, &be) && (be.Code == broker.CodeBadParams || be.Code == broker.CodeRefused):
			p.Blocked = append(p.Blocked, blocked(name, be.Message))
			return p, nil
		}
		return p, brokerError(err)
	}
	if pv.Running {
		p.Items = append(p.Items, actions.Item{ID: "stop", Name: name, Detail: "stopped, then started from the restored data"})
	}
	for _, v := range pv.Volumes {
		now := Size(v.Current) + " now"
		if v.Current < 0 {
			now = "size now unknown"
		}
		d := "volume, overwritten: " + now + ", " + Size(v.Archived) + " in the backup"
		if !v.Exists {
			d = "volume, created and filled: " + Size(v.Archived) + " in the backup"
		}
		p.Items = append(p.Items, actions.Item{ID: "volume:" + v.Name, Name: v.Name, Detail: d})
	}
	secrets := "replaced by the backup's"
	if len(pv.Secrets) > 0 {
		secrets += ": " + strings.Join(pv.Secrets, ", ")
	}
	p.Items = append(p.Items, actions.Item{ID: "secrets", Name: "secrets.env", Detail: secrets})
	switch {
	case pv.NewStack:
		p.Items = append(p.Items, actions.Item{ID: "spec", Name: "spec.json", Detail: "the app is made again from the backup's spec"})
	case pv.SpecChanged:
		p.Items = append(p.Items, actions.Item{ID: "spec", Name: "spec.json", Detail: "replaced by the backup's spec, which differs from today's"})
	}
	for _, f := range pv.Flags {
		p.Items = append(p.Items, actions.Item{ID: "flag:" + f, Name: f, Detail: "danger: " + FlagMeaning(f)})
	}
	for _, bind := range pv.Binds {
		p.Items = append(p.Items, actions.Item{ID: "bind:" + bind.Real, Name: bind.Real,
			Detail: "bind folder, not restored: copy it back by hand from the archive"})
	}
	if pv.Warning != "" {
		p.Items = append(p.Items, actions.Item{ID: "warning", Name: archive, Detail: pv.Warning})
	}
	p.Confirm = name
	p.Summary = "restore " + name + " from " + archive + ", overwriting its volumes"
	flags := pv.Flags
	if flags == nil {
		flags = []string{}
	}
	p.State = restoreState{Params: broker.StackRestoreParams{Name: name, Archive: archive, Token: pv.Token, TypedName: name, AckFlags: flags}}
	return p, nil
}

// HandleBackups serves GET /api/v1/app-backups/{name}: the archives,
// newest first, without their specs.
func HandleBackups(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !spec.ValidName(name) {
			jsonError(w, http.StatusBadRequest, "not a stack name")
			return
		}
		var out broker.BackupList
		if err := b.Call(r.Context(), "stack.backup.list", map[string]string{"name": name}, &out); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, out)
	}
}

// HandleBackupPreview serves GET /api/v1/app-backups/{name}/preview:
// sizes and free space first, with ?keep= and ?include=<path> (repeated).
func HandleBackupPreview(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params := map[string]string{}
		q := r.URL.Query()
		if k := q.Get("keep"); k != "" {
			params["keep"] = k
		}
		if inc := q["include"]; len(inc) > 0 {
			raw, _ := json.Marshal(inc)
			params["include"] = string(raw)
		}
		v, err := backupParams(actions.Request{Target: r.PathValue("name"), Params: params})
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		pv, err := previewBackup(r.Context(), b, v)
		if err != nil {
			var be *broker.Error
			if errors.As(err, &be) && (be.Code == broker.CodeBadParams || be.Code == broker.CodeRefused) {
				jsonError(w, http.StatusUnprocessableEntity, be.Message)
				return
			}
			httpError(w, err)
			return
		}
		writeJSON(w, pv)
	}
}
