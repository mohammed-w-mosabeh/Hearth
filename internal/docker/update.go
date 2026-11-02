package docker

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Pull and recreate a Compose project (ROADMAP 7.6, SECURITY S16h). The project
// is rendered here, as the user, and the same check the broker makes shows in
// the preview; what the preview showed is what goes to the broker.

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// TagChecker compares tags with their registry (Registry in production).
type TagChecker interface {
	Check(ctx context.Context, tags, repoDigests []string) []TagUpdate
}

type updateState struct {
	Params broker.ComposeUpdateParams
}

// RegisterUpdate adds project/update.
func RegisterUpdate(reg *actions.Registry, b Broker, tags TagChecker, run Runner) {
	reg.Register(actions.Action{Kind: "project", Name: "update", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 {
				return errors.New("params must be empty")
			}
			if !ValidProject(r.Target) {
				return errors.New("target must be a Compose project name")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planUpdate(ctx, b, tags, run, r.Target)
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
			st, ok := p.State.(updateState)
			if !ok {
				return res, nil
			}
			err := b.Call(ctx, "compose.update", st.Params, nil)
			if err == nil {
				res.Done = len(p.Items)
				return res, nil
			}
			item := actions.Item{ID: st.Params.Project, Name: st.Params.Project}
			bl, handled := failure(item, err)
			if !handled {
				return res, brokerError(err)
			}
			res.Failed = append(res.Failed, bl)
			return res, nil
		}})
}

func planUpdate(ctx context.Context, b Broker, tags TagChecker, run Runner, project string) (actions.Plan, error) {
	p := actions.Plan{Summary: "update " + project, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	block := func(name, reason string) {
		p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: project + "/" + name, Name: name}, Reason: reason})
	}
	var list []struct {
		ImageID string
		Labels  map[string]string
	}
	if err := b.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &list); err != nil {
		return p, brokerError(err)
	}
	running := map[string]string{} // service to image ID
	var dir string
	var files []string
	for _, c := range list {
		if c.Labels[labelProject] != project {
			continue
		}
		running[c.Labels[labelService]] = c.ImageID
		if len(files) == 0 {
			dir, files = discovery.ComposePlace(c.Labels)
		}
	}
	if len(running) == 0 {
		block(project, ReasonNotFound)
		return p, nil
	}
	if dir == "" || len(files) == 0 {
		block(project+": the project's files are not known", "refused")
		return p, nil
	}
	args := []string{"compose", "--project-name", project, "--project-directory", dir}
	for _, f := range files {
		args = append(args, "--file", f)
	}
	res, err := run(ctx, "docker-compose-config", append(args, "config", "--format", "json")...)
	if err != nil || res.ExitCode != 0 || res.Truncated {
		block(project+": its Compose files could not be rendered as this user", "refused")
		return p, nil
	}
	chk, err := broker.CheckCompose(res.Stdout, project)
	if err != nil {
		block(project+": "+err.Error(), "refused")
		return p, nil
	}
	for _, why := range chk.Refused {
		block(why, "refused")
	}
	names := make([]string, 0, len(chk.Services))
	for name := range chk.Services {
		names = append(names, name)
		if _, ok := running[name]; !ok {
			block(name+": new service, bring it up yourself once", "refused")
		}
	}
	if len(p.Blocked) > 0 {
		return p, nil // nothing runs unless all of it can
	}
	sort.Strings(names)
	for _, name := range names {
		image := chk.Services[name]
		p.Items = append(p.Items, actions.Item{ID: name, Name: name, Detail: image + ": " + digestChange(ctx, b, tags, running[name], image)})
	}
	p.State = updateState{Params: broker.ComposeUpdateParams{Project: project, Dir: dir, Files: files, Config: res.Stdout}}
	return p, nil
}

// digestChange says whether a service would get another image digest, in the
// daemon's English. What cannot be asked stays "unknown": it never reads as
// up to date.
func digestChange(ctx context.Context, b Broker, tags TagChecker, imageID, image string) string {
	var in struct{ RepoDigests []string }
	if err := b.Call(ctx, "docker.images.inspect", map[string]string{"id": imageID}, &in); err != nil {
		return "digest unknown"
	}
	got := tags.Check(ctx, []string{image}, in.RepoDigests)
	if len(got) != 1 {
		return "digest unknown"
	}
	switch got[0].Status {
	case UpdateCurrent:
		return "digest unchanged"
	case UpdateNewer:
		return "new digest " + shortDigest(got[0].Remote)
	case UpdateLocal:
		return "built here or never pulled"
	}
	return "digest unknown"
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
