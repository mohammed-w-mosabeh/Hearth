package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Images page (ROADMAP 2.6): detail (inspect + layer history), the
// registry update check, the optional Trivy scan, and image actions.
// Routes take the image ID ("sha256:<hex>"), never a reference.

var imageIDRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ImageDetail is GET /api/v1/docker/images/{id}.
type ImageDetail struct {
	Inspect json.RawMessage `json:"inspect"` // env masked by the broker
	History json.RawMessage `json:"history"` // newest layer first
}

// Images serves the image routes.
type Images struct {
	B        Broker
	Registry *Registry
	// Run runs an allowlisted command (safeexec.Run in production).
	Run func(ctx context.Context, name string, args ...string) (safeexec.Result, error)
	// CacheDir is trivy's cache, "<data home>/hearth/trivy".
	CacheDir string
}

func imageID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !imageIDRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, "not an image ID")
		return "", false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func brokerStatus(err error) int {
	var be *broker.Error
	switch {
	case errors.As(err, &be) && be.Code == broker.CodeNotFound:
		return http.StatusNotFound
	case errors.Is(err, broker.ErrUnavailable), errors.Is(err, broker.ErrNoDocker):
		return http.StatusServiceUnavailable
	case errors.Is(err, broker.ErrNotMember):
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}

// HandleDetail serves GET /api/v1/docker/images/{id}.
func (x *Images) HandleDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var d ImageDetail
	err := x.B.Call(ctx, "docker.images.inspect", map[string]string{"id": id}, &d.Inspect)
	if err == nil {
		err = x.B.Call(ctx, "docker.images.history", map[string]string{"id": id}, &d.History)
	}
	if err != nil {
		writeError(w, brokerStatus(err), errorText(err))
		return
	}
	writeJSON(w, d)
}

// HandleUpdates serves GET /api/v1/docker/images/{id}/updates: each tag
// compared with its registry (SECURITY S21a). Only when asked.
func (x *Images) HandleUpdates(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var in struct {
		RepoTags    []string
		RepoDigests []string
	}
	if err := x.B.Call(ctx, "docker.images.inspect", map[string]string{"id": id}, &in); err != nil {
		writeError(w, brokerStatus(err), errorText(err))
		return
	}
	if len(in.RepoTags) > 8 {
		in.RepoTags = in.RepoTags[:8]
	}
	writeJSON(w, map[string]any{"tags": x.Registry.Check(ctx, in.RepoTags, in.RepoDigests)})
}

// Trivy is GET /api/v1/docker/trivy.
type Trivy struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
}

// HandleTrivy serves GET /api/v1/docker/trivy.
func (x *Images) HandleTrivy(w http.ResponseWriter, r *http.Request) {
	var t Trivy
	if res, err := x.Run(r.Context(), "trivy-version", "--version"); err == nil {
		t.Installed = true
		first, _, _ := strings.Cut(string(res.Stdout), "\n")
		t.Version = strings.TrimSpace(strings.TrimPrefix(first, "Version:"))
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, t)
}

// Vuln is one vulnerability Trivy found.
type Vuln struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed,omitempty"`
	Severity  string `json:"severity"`
}

// Scan is POST /api/v1/docker/images/{id}/scan.
type Scan struct {
	Counts    map[string]int `json:"counts"` // by severity
	Vulns     []Vuln         `json:"vulns"`  // most severe first, at most maxVulns
	Truncated bool           `json:"truncated"`
}

const maxVulns = 500

var severityOrder = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}

var vulnIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

// parseTrivy reads safeexec.TrivyTemplate output.
func parseTrivy(out []byte) Scan {
	s := Scan{Counts: map[string]int{}, Vulns: []Vuln{}}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 5 || !vulnIDRe.MatchString(f[0]) {
			continue
		}
		v := Vuln{ID: f[0], Package: truncate(f[1], 200), Installed: truncate(f[2], 100), Fixed: truncate(f[3], 200), Severity: f[4]}
		if _, ok := severityOrder[v.Severity]; !ok {
			v.Severity = "UNKNOWN"
		}
		k := v.ID + "|" + v.Package + "|" + v.Installed
		if seen[k] {
			continue
		}
		seen[k] = true
		s.Counts[v.Severity]++
		s.Vulns = append(s.Vulns, v)
	}
	sort.SliceStable(s.Vulns, func(i, j int) bool { return severityOrder[s.Vulns[i].Severity] < severityOrder[s.Vulns[j].Severity] })
	if len(s.Vulns) > maxVulns {
		s.Vulns, s.Truncated = s.Vulns[:maxVulns], true
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "?")
}

// HandleScan serves POST /api/v1/docker/images/{id}/scan: Trivy on the
// image's first tag (or its ID). It can take minutes the first time
// (Trivy downloads its database).
func (x *Images) HandleScan(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var in struct{ RepoTags []string }
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	err := x.B.Call(ctx, "docker.images.inspect", map[string]string{"id": id}, &in)
	cancel()
	if err != nil {
		writeError(w, brokerStatus(err), errorText(err))
		return
	}
	target := id
	if len(in.RepoTags) > 0 && safeexec.ImageRef(in.RepoTags[0]) == nil {
		target = in.RepoTags[0]
	}
	res, err := x.Run(r.Context(), "trivy-image", "image", "--quiet", "--scanners=vuln", "--timeout=9m",
		"--format=template", "--template="+safeexec.TrivyTemplate, "--cache-dir="+x.CacheDir, target)
	switch {
	case errors.Is(err, safeexec.ErrBinary):
		writeError(w, http.StatusNotFound, "trivy is not installed (or not a root-owned binary)")
		return
	case err != nil:
		msg := strings.TrimSpace(string(res.Stderr))
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		writeError(w, http.StatusBadGateway, "trivy: "+truncate(msg, 300))
		return
	}
	s := parseTrivy(res.Stdout)
	s.Truncated = s.Truncated || res.Truncated
	writeJSON(w, s)
}

// --- image actions ---

// imageState is what a plan saw of an image.
type imageState struct {
	ID    string
	Tags  []string
	Users []string // containers using it
	Force bool
}

func imageName(st imageState) string {
	if len(st.Tags) > 0 {
		return st.Tags[0]
	}
	return st.ID[len("sha256:") : len("sha256:")+12]
}

// RegisterImages adds image/remove, image/tag and image/pull to reg.
// remove is destructive: an image in use needs its name typed back and
// is removed by force (its containers keep running from it).
func RegisterImages(reg *actions.Registry, b Broker) {
	validID := func(r actions.Request) error {
		if !imageIDRe.MatchString(r.Target) {
			return errors.New("target must be an image ID")
		}
		return nil
	}
	reg.Register(actions.Action{Kind: "image", Name: "remove", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 {
				return errors.New("params must be empty")
			}
			return validID(r)
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planImageRemove(ctx, b, r.Target)
		},
		Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
			st, _ := p.State.(imageState)
			return runImage(ctx, b, p, "docker.images.remove", map[string]any{"id": st.ID, "force": st.Force})
		}})
	reg.Register(actions.Action{Kind: "image", Name: "tag", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if err := validID(r); err != nil {
				return err
			}
			if len(r.Params) != 2 || !broker.ValidImageName(r.Params["repo"]) || !broker.ValidImageTag(r.Params["tag"]) {
				return errors.New("params must be a valid repo and tag")
			}
			return nil
		},
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			return planImage(ctx, b, "tag", r.Target)
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			st, _ := p.State.(imageState)
			return runImage(ctx, b, p, "docker.images.tag", map[string]string{"id": st.ID, "repo": r.Params["repo"], "tag": r.Params["tag"]})
		}})
	reg.Register(actions.Action{Kind: "image", Name: "pull", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if len(r.Params) != 0 {
				return errors.New("params must be empty")
			}
			if _, _, _, ok := broker.ParseImageRef(r.Target); !ok {
				return errors.New("target must be an image reference")
			}
			return nil
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			it := actions.Item{ID: r.Target, Name: r.Target}
			return actions.Plan{Summary: "pull " + r.Target, Items: []actions.Item{it}, Blocked: []actions.Blocked{}, State: imageState{ID: r.Target}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return runImage(ctx, b, p, "docker.images.pull", map[string]string{"ref": r.Target})
		}})
}

func inspectImage(ctx context.Context, b Broker, id string) (imageState, error) {
	var in struct {
		ID       string `json:"Id"`
		RepoTags []string
	}
	if err := b.Call(ctx, "docker.images.inspect", map[string]string{"id": id}, &in); err != nil {
		return imageState{}, err
	}
	return imageState{ID: in.ID, Tags: in.RepoTags}, nil
}

func planImage(ctx context.Context, b Broker, op, id string) (actions.Plan, error) {
	p := actions.Plan{Summary: op + " " + id, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	st, err := inspectImage(ctx, b, id)
	if err != nil {
		if bl, ok := failure(actions.Item{ID: id, Name: id[:19]}, err); ok {
			p.Blocked = append(p.Blocked, bl)
			return p, nil
		}
		return p, brokerError(err)
	}
	p.Summary = op + " " + imageName(st)
	p.Items = append(p.Items, actions.Item{ID: st.ID, Name: imageName(st), Detail: strings.Join(st.Tags, ", ")})
	p.State = st
	return p, nil
}

func planImageRemove(ctx context.Context, b Broker, id string) (actions.Plan, error) {
	p, err := planImage(ctx, b, "remove", id)
	st, ok := p.State.(imageState)
	if err != nil || !ok {
		return p, err
	}
	var cs []struct {
		ID      string `json:"Id"`
		Names   []string
		ImageID string
	}
	if err := b.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &cs); err != nil {
		return p, brokerError(err)
	}
	for _, c := range cs {
		if c.ImageID == st.ID && len(c.Names) > 0 {
			st.Users = append(st.Users, strings.TrimPrefix(c.Names[0], "/"))
		}
	}
	// Docker refuses to remove an image used by containers or tagged more
	// than once without force.
	st.Force = len(st.Users) > 0 || len(st.Tags) > 1
	if len(st.Users) > 0 {
		p.Confirm = imageName(st)
		for _, u := range st.Users {
			p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: "container:" + u, Name: u}, Reason: "in-use"})
		}
	}
	p.State = st
	return p, nil
}

func runImage(ctx context.Context, b Broker, p actions.Plan, method string, params any) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
	if len(p.Items) == 0 {
		res.Blocked = p.Blocked
		return res, nil
	}
	if err := b.Call(ctx, method, params, nil); err != nil {
		bl, ok := failure(p.Items[0], err)
		if !ok {
			return res, brokerError(err)
		}
		res.Failed = append(res.Failed, bl)
		return res, nil
	}
	res.Done = 1
	return res, nil
}
