package docker

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Compose projects (SPEC 5, ROADMAP 2.7): start, stop and restart every
// container labelled with a project, one broker call per container (there
// is no compose CLI in the broker). Containers start in the order of
// Compose's depends_on label, dependencies first, and stop in reverse.

// projectRe is a Compose project name (lowercase letters, digits, "-", "_").
var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// ValidProject reports whether s can be a Compose project name.
func ValidProject(s string) bool { return projectRe.MatchString(s) }

// member is one container of a project.
type member struct {
	ID, Name, Service, State string
	Deps                     []string // services it depends on
}

type projectState struct {
	Members []member // in start order
}

// RegisterProjects adds project/start, project/stop and project/restart.
func RegisterProjects(reg *actions.Registry, b Broker) {
	for _, o := range []op{
		{name: "start", risk: actions.Safe},
		{name: "stop", risk: actions.Reversible},
		{name: "restart", risk: actions.Reversible},
	} {
		reg.Register(actions.Action{Kind: "project", Name: o.name, Risk: o.risk,
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
				return planProject(ctx, b, o.name, r.Target)
			},
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return runProject(ctx, b, o.name, p)
			}})
	}
}

func planProject(ctx context.Context, b Broker, op, project string) (actions.Plan, error) {
	p := actions.Plan{Summary: op + " " + project, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	var list []struct {
		ID     string `json:"Id"`
		Names  []string
		State  string
		Labels map[string]string
	}
	if err := b.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &list); err != nil {
		return p, brokerError(err)
	}
	var ms []member
	for _, c := range list {
		if c.Labels[labelProject] != project || len(c.Names) == 0 {
			continue
		}
		ms = append(ms, member{
			ID: c.ID, Name: strings.TrimPrefix(c.Names[0], "/"), State: c.State,
			Service: c.Labels[labelService], Deps: dependsOn(c.Labels[labelDependsOn]),
		})
	}
	if len(ms) == 0 {
		p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: project, Name: project}, Reason: ReasonNotFound})
		return p, nil
	}
	ms = startOrder(ms)
	if op == "stop" {
		ms = reversed(ms)
	}
	for _, m := range ms {
		p.Items = append(p.Items, actions.Item{ID: m.ID, Name: m.Name, Detail: m.State})
	}
	p.State = projectState{Members: ms}
	return p, nil
}

// Compose labels read here (discovery has the others).
const (
	labelProject   = "com.docker.compose.project"
	labelService   = "com.docker.compose.service"
	labelDependsOn = "com.docker.compose.depends_on" // "svc:condition:restart,..."
)

func dependsOn(label string) []string {
	var out []string
	for _, d := range strings.Split(label, ",") {
		if svc, _, _ := strings.Cut(strings.TrimSpace(d), ":"); svc != "" {
			out = append(out, svc)
		}
	}
	return out
}

// startOrder sorts members so each comes after the services it depends
// on; ties (and cycles) by name.
func startOrder(ms []member) []member {
	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	bySvc := map[string][]int{}
	for i, m := range ms {
		bySvc[m.Service] = append(bySvc[m.Service], i)
	}
	const (
		unvisited = iota
		visiting
		done
	)
	mark := make([]int, len(ms))
	out := make([]member, 0, len(ms))
	var visit func(i int)
	visit = func(i int) {
		if mark[i] != unvisited {
			return // done, or a cycle: break it here
		}
		mark[i] = visiting
		for _, d := range ms[i].Deps {
			for _, j := range bySvc[d] {
				visit(j)
			}
		}
		mark[i] = done
		out = append(out, ms[i])
	}
	for i := range ms {
		visit(i)
	}
	return out
}

func reversed(ms []member) []member {
	out := make([]member, len(ms))
	for i, m := range ms {
		out[len(ms)-1-i] = m
	}
	return out
}

// runProject acts on each member in plan order. restart is stop (reverse
// order) then start, so dependencies come back first. A failure is
// reported per container; the others still run.
func runProject(ctx context.Context, b Broker, op string, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
	st, ok := p.State.(projectState)
	if !ok || len(st.Members) == 0 {
		return res, nil
	}
	steps := []struct {
		method  string
		members []member
	}{{"docker.containers." + op, st.Members}}
	if op == "restart" {
		steps = []struct {
			method  string
			members []member
		}{{"docker.containers.stop", reversed(st.Members)}, {"docker.containers.start", st.Members}}
	}
	failed := map[string]bool{}
	for _, s := range steps {
		for _, m := range s.members {
			if failed[m.ID] {
				continue
			}
			err := b.Call(ctx, s.method, map[string]string{"id": m.ID}, nil)
			if err == nil {
				continue
			}
			bl, ok := failure(actions.Item{ID: m.ID, Name: m.Name, Detail: m.Service}, err)
			if !ok {
				return res, brokerError(err)
			}
			if errors.Is(err, broker.ErrUnauthorized) {
				// Refused once means refused for all: do not ask again per container.
				res.Failed = append(res.Failed, bl)
				return res, nil
			}
			failed[m.ID] = true
			res.Failed = append(res.Failed, bl)
		}
	}
	res.Done = len(st.Members) - len(failed)
	return res, nil
}
