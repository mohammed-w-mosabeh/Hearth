package importer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Broker calls hearth-broker.
type Broker interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// Deps are what a dry-run reads.
type Deps struct {
	Broker Broker
	Run    Runner
	// TZ returns the host's time zone; nil reads /etc/timezone.
	TZ func() string
}

// Container is one container of the project, as it is now.
type Container struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
}

// VolumeUse says whether a volume of the spec exists, and is reused.
type VolumeUse struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

// PortUse is one host port of the spec, with what else holds it.
type PortUse struct {
	Service  string `json:"service"`
	HostIP   string `json:"host_ip"`
	Host     int    `json:"host"`
	Protocol string `json:"protocol"`
	// Conflict names another container that publishes the port.
	Conflict string `json:"conflict,omitempty"`
}

// DryRunResult is what an import would do. Nothing was changed to find it,
// and no secret value is in it.
type DryRunResult struct {
	Project    string      `json:"project"`
	Dir        string      `json:"dir"`
	Files      []string    `json:"files"`
	Containers []Container `json:"containers"`
	Result
	Volumes []VolumeUse `json:"volumes"`
	Ports   []PortUse   `json:"ports"`
	Notes   []string    `json:"notes"`
}

type listed struct {
	ID     string `json:"Id"`
	Names  []string
	State  string
	Labels map[string]string
	Ports  []struct {
		IP         string
		PublicPort uint16
		Type       string
	}
}

// ErrNoProject means no container belongs to the project.
var ErrNoProject = errors.New("no Compose project by that name has containers")

// DryRun converts project as an import would, and changes nothing: it
// lists containers and volumes and renders the project as the user.
func DryRun(ctx context.Context, d Deps, project string, plain, skip map[string]bool) (DryRunResult, error) {
	out := DryRunResult{Project: project, Files: []string{}, Containers: []Container{}, Volumes: []VolumeUse{}, Ports: []PortUse{}}
	if !spec.ValidName(project) {
		out.Result = Convert(Input{Project: project})
		return out, nil
	}
	var all []listed
	if err := d.Broker.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &all); err != nil {
		return out, err
	}
	var mine, others []listed
	for _, c := range all {
		if c.Labels["com.docker.compose.project"] == project {
			mine = append(mine, c)
		} else {
			others = append(others, c)
		}
	}
	if len(mine) == 0 {
		return out, ErrNoProject
	}
	managed := false
	for _, c := range mine {
		out.Containers = append(out.Containers, Container{Name: containerName(c), Service: c.Labels["com.docker.compose.service"], State: c.State})
		if c.Labels[spec.LabelStack] != "" {
			managed = true
		}
		if out.Dir == "" {
			out.Dir, out.Files = discovery.ComposePlace(c.Labels)
		}
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].Name < out.Containers[j].Name })
	if out.Files == nil {
		out.Files = []string{}
	}
	if managed {
		out.Result = refusedOnly("project", "Hearth already manages it")
		return out, nil
	}
	if out.Dir == "" || len(out.Files) == 0 {
		out.Result = refusedOnly("project", "its folder and files are not known from its containers")
		return out, nil
	}
	args := []string{"compose", "--project-name", project, "--project-directory", out.Dir}
	for _, f := range out.Files {
		args = append(args, "--file", f)
	}
	res, err := d.Run(ctx, "docker-compose-config", append(args, "config", "--format", "json")...)
	if err != nil || res.ExitCode != 0 || res.Truncated {
		// Compose's message is not shown: it can quote the project's values.
		out.Result = refusedOnly("project", "Compose could not render it as this user (a remote include or extends is refused, and so is a missing file or variable)")
		return out, nil
	}
	tz := readTZ
	if d.TZ != nil {
		tz = d.TZ
	}
	out.Result = Convert(Input{Project: project, Rendered: res.Stdout, Plain: plain, Skip: skip, TZ: tz()})
	out.Result = displayed(out.Result)

	var vols struct {
		Volumes []struct{ Name string }
	}
	if err := d.Broker.Call(ctx, "docker.volumes.list", nil, &vols); err != nil {
		return out, err
	}
	have := map[string]bool{}
	for _, v := range vols.Volumes {
		have[v.Name] = true
	}
	for _, k := range sortedKeys(out.Spec.Volumes) {
		name := out.Spec.Volumes[k].Name
		if name == "" {
			name = project + "_" + k
		}
		out.Volumes = append(out.Volumes, VolumeUse{Key: k, Name: name, Exists: have[name]})
	}
	for _, svc := range sortedKeys(out.Spec.Services) {
		for _, p := range out.Spec.Services[svc].Ports {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			use := PortUse{Service: svc, HostIP: p.HostIP, Host: p.Host, Protocol: proto}
			for _, o := range others {
				for _, op := range o.Ports {
					if int(op.PublicPort) == p.Host && op.Type == proto && overlaps(op.IP, p.HostIP) {
						use.Conflict = containerName(o)
					}
				}
			}
			out.Ports = append(out.Ports, use)
			if use.Conflict != "" {
				out.Refused = append(out.Refused, svc+": port "+strconv.Itoa(p.Host)+"/"+proto+" is held by "+use.Conflict)
			}
		}
	}
	out.Notes = []string{
		"Nothing was changed to make this dry-run.",
		"On import, Compose recreates the containers once, as their configuration changes; volumes and bind folders stay where they are.",
	}
	return out, nil
}

func refusedOnly(where, why string) Result {
	return Result{Refused: []string{where + ": " + why}, Changes: []string{}, Flags: []string{}, Secrets: []SecretKey{}}
}

// displayed masks the plain environment values S19 masks, as status does,
// wherever they appear: the owner marked them plain, so the spec keeps
// them, but the page does not show them.
func displayed(r Result) Result {
	var hide []string
	for _, svc := range r.Spec.Services {
		for k, v := range svc.Environment {
			if (secret.Name(k) || secret.Value(v)) && v != "" {
				hide = append(hide, v)
			}
		}
	}
	if len(hide) == 0 {
		return r
	}
	sort.Slice(hide, func(i, j int) bool { return len(hide[i]) > len(hide[j]) }) // longest first
	raw, err := json.Marshal(r.Spec)
	if err != nil {
		r.Spec = spec.Spec{Version: spec.Version, Name: r.Spec.Name}
		return r
	}
	s := string(raw)
	for _, v := range hide {
		ev, _ := json.Marshal(v)
		s = strings.ReplaceAll(s, string(ev[1:len(ev)-1]), secret.Mask)
		for i := range r.Refused {
			r.Refused[i] = strings.ReplaceAll(r.Refused[i], v, secret.Mask)
		}
		for i := range r.Changes {
			r.Changes[i] = strings.ReplaceAll(r.Changes[i], v, secret.Mask)
		}
	}
	var masked spec.Spec
	if err := json.Unmarshal([]byte(s), &masked); err != nil {
		masked = spec.Spec{Version: spec.Version, Name: r.Spec.Name}
	}
	r.Spec = masked
	return r
}

func containerName(c listed) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// overlaps: two host addresses can hold the same port at once only when
// both are specific and differ.
func overlaps(a, b string) bool {
	wild := func(s string) bool { return s == "" || s == "0.0.0.0" || s == "::" }
	return wild(a) || wild(b) || a == b
}

// readTZ is the host's zone: /etc/timezone, or the zoneinfo name that
// /etc/localtime links to (Debian 13 may have no /etc/timezone).
func readTZ() string {
	tz := ""
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		tz = strings.TrimSpace(string(b))
	} else if l, err := os.Readlink("/etc/localtime"); err == nil {
		_, tz, _ = strings.Cut(l, "zoneinfo/")
	}
	if !tzName.MatchString(tz) {
		return ""
	}
	return tz
}

var tzName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]{0,30}(/[A-Za-z0-9_+-]{1,30}){0,2}$`)

// Handle serves GET /api/v1/stacks/import/{project}?plain=A,B&skip=svc: the
// dry-run, which writes nothing and shows no secret value.
func Handle(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project := r.PathValue("project")
		plain, skip := set(r.URL.Query().Get("plain")), set(r.URL.Query().Get("skip"))
		if len(plain) > 256 || len(skip) > 64 {
			jsonError(w, http.StatusBadRequest, "too many names")
			return
		}
		res, err := DryRun(r.Context(), d, project, plain, skip)
		switch {
		case errors.Is(err, ErrNoProject):
			jsonError(w, http.StatusNotFound, err.Error())
			return
		case errors.Is(err, broker.ErrNotMember):
			jsonError(w, http.StatusForbidden, "you are not in the hearth group")
			return
		case errors.Is(err, broker.ErrUnavailable), errors.Is(err, broker.ErrNoDocker):
			jsonError(w, http.StatusServiceUnavailable, "hearth-broker or docker is not available")
			return
		case err != nil:
			jsonError(w, http.StatusBadGateway, "the broker could not be asked")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
	}
}

func set(s string) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" && len(k) <= 128 {
			out[k] = true
		}
	}
	return out
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
