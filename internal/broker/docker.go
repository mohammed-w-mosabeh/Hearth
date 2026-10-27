package broker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The Docker proxy (SECURITY S12). The broker speaks to the Engine API on
// its socket (which is root) and offers only the methods below: each
// sends one fixed kind of request, with validated, escaped arguments.
// There is no passthrough, no exec, no container create and no way to
// choose a path, so a caller cannot reach any other part of the API.

// DockerSocket is the Engine's socket (/var/run is /run on Debian).
const DockerSocket = "/run/docker.sock"

// DockerAPI is the Engine API version requests use: Docker 25 and later
// (Docker 29 refuses anything older than 1.44).
const DockerAPI = "v1.44"

// polkit actions for Docker changes. Control changes a container's state
// or adds an image; remove deletes something.
const (
	ActionDockerControl = "org.hearth.docker.control"
	ActionDockerRemove  = "org.hearth.docker.remove"
)

// maxDockerBody bounds a Docker answer the broker forwards, leaving room
// in MaxResponse for the envelope.
const maxDockerBody = 12 << 20

// Docker is a client for the Engine API on a Unix socket.
type Docker struct {
	hc  *http.Client
	now func() time.Time
}

// NewDocker returns a client for the Engine at socket.
func NewDocker(socket string) *Docker {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:       2,
		IdleConnTimeout:    10 * time.Second,
		DisableCompression: true,
	}
	return &Docker{
		hc: &http.Client{
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}
}

// send makes one request. path must already be escaped. A 304 (already
// started, already stopped) is success with no body.
func (d *Docker) send(ctx context.Context, method, path string, q url.Values) (*http.Response, error) {
	u := "http://docker/" + DockerAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, Errorf(CodeInternal, "bad request")
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, Errorf(CodeFailed, "docker: timed out")
		}
		return nil, Errorf(CodeNoDocker, "docker is not available")
	}
	if resp.StatusCode >= 400 || (resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified) {
		defer func() { _ = resp.Body.Close() }()
		return nil, dockerError(resp)
	}
	return resp, nil
}

// do makes one request and reads its whole answer.
func (d *Docker) do(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	resp, err := d.send(ctx, method, path, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDockerBody+1))
	if err != nil {
		return nil, Errorf(CodeFailed, "docker: reading the answer failed")
	}
	if len(b) > maxDockerBody {
		return nil, Errorf(CodeFailed, "docker: answer too large")
	}
	return b, nil
}

// get returns a JSON answer as is.
func (d *Docker) get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	b, err := d.do(ctx, http.MethodGet, path, q)
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, Errorf(CodeFailed, "docker: answer is not JSON")
	}
	return b, nil
}

// getInto decodes a JSON answer into v.
func (d *Docker) getInto(ctx context.Context, path string, q url.Values, v any) error {
	b, err := d.do(ctx, http.MethodGet, path, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return Errorf(CodeFailed, "docker: unexpected answer")
	}
	return nil
}

func dockerError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var e struct{ Message string }
	msg := resp.Status
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	msg = strings.ToValidUTF8(msg, "?")
	code := CodeFailed
	switch resp.StatusCode {
	case http.StatusNotFound:
		code = CodeNotFound
	case http.StatusConflict:
		code = CodeConflict
	}
	return Errorf(code, "docker: %s", msg)
}

// decodeParams decodes p into v, refusing unknown fields and trailing
// data; empty or null params leave v at its zero value.
func decodeParams(p json.RawMessage, v any) error {
	if len(p) == 0 || string(p) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return Errorf(CodeBadParams, "bad params")
	}
	return nil
}

func badParam(format string, a ...any) error { return Errorf(CodeBadParams, format, a...) }

func esc(s string) string { return url.PathEscape(s) }

func boolQ(b bool) string { return strconv.FormatBool(b) }

func filtersQ(f map[string][]string) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// DockerMethods are the broker's Docker methods.
func DockerMethods(d *Docker) []Method {
	read := func(name string, h func(context.Context, json.RawMessage) (any, error)) Method {
		return Method{Name: name, Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) { return h(ctx, p) }}
	}
	change := func(name, action string, timeout time.Duration, h func(context.Context, json.RawMessage) (any, error)) Method {
		m := read(name, h)
		m.Privileged, m.Action, m.Timeout = true, action, timeout
		return m
	}
	plain := func(path string) func(context.Context, json.RawMessage) (any, error) {
		return func(ctx context.Context, p json.RawMessage) (any, error) {
			var none struct{}
			if err := decodeParams(p, &none); err != nil {
				return nil, err
			}
			return d.get(ctx, path, nil)
		}
	}
	const stopWait = 11 * time.Minute // a stop timeout of up to 600 s
	return []Method{
		read("docker.version", plain("/version")),
		read("docker.info", plain("/info")),
		read("docker.df", plain("/system/df")),
		read("docker.containers.list", d.containersList),
		read("docker.containers.inspect", d.containerInspect),
		read("docker.containers.stats", d.containerStats),
		read("docker.containers.logs", d.containerLogs),
		read("docker.images.list", plain("/images/json")),
		read("docker.images.inspect", d.imageInspect),
		read("docker.images.history", d.imageHistory),
		read("docker.networks.list", plain("/networks")),
		read("docker.networks.inspect", d.networkInspect),
		read("docker.volumes.list", plain("/volumes")),
		read("docker.volumes.inspect", d.volumeInspect),
		read("docker.prune.preview", d.prunePreview),
		{Name: "docker.events", Stream: d.dockerEvents},

		change("docker.containers.start", ActionDockerControl, 0, d.containerAction("start")),
		change("docker.containers.stop", ActionDockerControl, stopWait, d.containerAction("stop")),
		change("docker.containers.restart", ActionDockerControl, stopWait, d.containerAction("restart")),
		change("docker.containers.pause", ActionDockerControl, 0, d.containerAction("pause")),
		change("docker.containers.unpause", ActionDockerControl, 0, d.containerAction("unpause")),
		change("docker.images.tag", ActionDockerControl, 0, d.imageTag),
		change("docker.images.pull", ActionDockerControl, 30*time.Minute, d.imagePull),

		change("docker.containers.remove", ActionDockerRemove, stopWait, d.containerRemove),
		change("docker.images.remove", ActionDockerRemove, 0, d.imageRemove),
		change("docker.networks.remove", ActionDockerRemove, 0, d.networkRemove),
		change("docker.volumes.remove", ActionDockerRemove, 0, d.volumeRemove),
		change("docker.prune", ActionDockerRemove, 30*time.Minute, d.prune),
	}
}

// --- containers ---

type idParams struct {
	ID string `json:"id"`
}

func containerID(p json.RawMessage) (string, error) {
	var v idParams
	if err := decodeParams(p, &v); err != nil {
		return "", err
	}
	if !ValidContainer(v.ID) {
		return "", badParam("id must be a container ID or name")
	}
	return v.ID, nil
}

func (d *Docker) containersList(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		All  bool `json:"all"`
		Size bool `json:"size"`
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	return d.get(ctx, "/containers/json", url.Values{"all": {boolQ(v.All)}, "size": {boolQ(v.Size)}})
}

// ContainerInspect is docker.containers.inspect's result: Docker's
// inspect with secret env values masked, and the container's S12 risks.
type ContainerInspect struct {
	Inspect json.RawMessage `json:"inspect"`
	Risks   []string        `json:"risks"`
}

func (d *Docker) containerInspect(ctx context.Context, p json.RawMessage) (any, error) {
	id, err := containerID(p)
	if err != nil {
		return nil, err
	}
	raw, err := d.get(ctx, "/containers/"+esc(id)+"/json", nil)
	if err != nil {
		return nil, err
	}
	var hc struct{ HostConfig HostConfig }
	if err := json.Unmarshal(raw, &hc); err != nil {
		return nil, Errorf(CodeFailed, "docker: unexpected answer")
	}
	masked, err := maskEnvIn(raw, "Config")
	if err != nil {
		return nil, err
	}
	risks := Risks(hc.HostConfig)
	if risks == nil {
		risks = []string{}
	}
	return ContainerInspect{Inspect: masked, Risks: risks}, nil
}

// maskEnvIn masks <key>.Env of a JSON object for each key, leaving the
// rest of it as Docker sent it.
func maskEnvIn(raw json.RawMessage, keys ...string) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, Errorf(CodeFailed, "docker: unexpected answer")
	}
	for _, k := range keys {
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(top[k], &cfg); err != nil || cfg == nil {
			continue // absent or null
		}
		var env []string
		if err := json.Unmarshal(cfg["Env"], &env); err != nil || env == nil {
			continue
		}
		cfg["Env"], _ = json.Marshal(MaskEnv(env))
		top[k], _ = json.Marshal(cfg)
	}
	out, err := json.Marshal(top)
	if err != nil {
		return nil, Errorf(CodeInternal, "result could not be encoded")
	}
	return out, nil
}

func (d *Docker) containerStats(ctx context.Context, p json.RawMessage) (any, error) {
	id, err := containerID(p)
	if err != nil {
		return nil, err
	}
	return d.get(ctx, "/containers/"+esc(id)+"/stats", url.Values{"stream": {"false"}, "one-shot": {"true"}})
}

// LogLine is one line of container output.
type LogLine struct {
	Stream string `json:"stream"` // stdout or stderr
	Text   string `json:"text"`
}

// Logs is docker.containers.logs's result.
type Logs struct {
	Lines     []LogLine `json:"lines"`
	Truncated bool      `json:"truncated"` // more output than the broker returns
}

const maxLogBytes = 4 << 20

func (d *Docker) containerLogs(ctx context.Context, p json.RawMessage) (any, error) {
	v := struct {
		ID         string `json:"id"`
		Tail       int    `json:"tail"`
		Since      int64  `json:"since"`
		Timestamps bool   `json:"timestamps"`
	}{Tail: 200}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !ValidContainer(v.ID) {
		return nil, badParam("id must be a container ID or name")
	}
	if v.Tail < 1 || v.Tail > 5000 {
		return nil, badParam("tail must be 1–5000")
	}
	if v.Since < 0 {
		return nil, badParam("since must be a Unix time")
	}
	var info struct{ Config struct{ Tty bool } }
	if err := d.getInto(ctx, "/containers/"+esc(v.ID)+"/json", nil, &info); err != nil {
		return nil, err
	}
	q := url.Values{
		"stdout": {"true"}, "stderr": {"true"}, "tail": {strconv.Itoa(v.Tail)},
		"since": {strconv.FormatInt(v.Since, 10)}, "timestamps": {boolQ(v.Timestamps)},
	}
	resp, err := d.send(ctx, http.MethodGet, "/containers/"+esc(v.ID)+"/logs", q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes+1))
	if err != nil {
		return nil, Errorf(CodeFailed, "docker: reading logs failed")
	}
	out := Logs{Lines: []LogLine{}}
	if len(b) > maxLogBytes {
		b, out.Truncated = b[:maxLogBytes], true
	}
	if info.Config.Tty {
		out.Lines = splitLog(out.Lines, "stdout", b)
	} else {
		out.Lines = demuxLog(out.Lines, b)
	}
	return out, nil
}

// demuxLog splits Docker's multiplexed stream: frames of an 8-byte header
// (stream byte, 3 zero bytes, big-endian length) and the payload. A
// cut-off last frame is dropped.
func demuxLog(out []LogLine, b []byte) []LogLine {
	for len(b) >= 8 {
		n := int64(binary.BigEndian.Uint32(b[4:8]))
		if n > int64(len(b)-8) {
			break
		}
		stream := "stdout"
		if b[0] == 2 {
			stream = "stderr"
		}
		out = splitLog(out, stream, b[8:8+n])
		b = b[8+n:]
	}
	return out
}

func splitLog(out []LogLine, stream string, b []byte) []LogLine {
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if l == "" {
			continue
		}
		l = strings.TrimRight(l, "\r\n")
		if !utf8.ValidString(l) {
			l = strings.ToValidUTF8(l, "�")
		}
		out = append(out, LogLine{Stream: stream, Text: l})
	}
	return out
}

func (d *Docker) containerAction(action string) func(context.Context, json.RawMessage) (any, error) {
	timed := action == "stop" || action == "restart"
	return func(ctx context.Context, p json.RawMessage) (any, error) {
		var v struct {
			ID      string `json:"id"`
			Timeout *int   `json:"timeout"`
		}
		if err := decodeParams(p, &v); err != nil {
			return nil, err
		}
		if !ValidContainer(v.ID) {
			return nil, badParam("id must be a container ID or name")
		}
		q := url.Values{}
		switch {
		case v.Timeout != nil && !timed:
			return nil, badParam("%s takes no timeout", action)
		case v.Timeout != nil && (*v.Timeout < 0 || *v.Timeout > 600):
			return nil, badParam("timeout must be 0–600 seconds")
		case v.Timeout != nil:
			q.Set("t", strconv.Itoa(*v.Timeout))
		}
		if _, err := d.do(ctx, http.MethodPost, "/containers/"+esc(v.ID)+"/"+action, q); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}
}

func (d *Docker) containerRemove(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		ID      string `json:"id"`
		Force   bool   `json:"force"`   // remove a running container
		Volumes bool   `json:"volumes"` // and its anonymous volumes
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !ValidContainer(v.ID) {
		return nil, badParam("id must be a container ID or name")
	}
	q := url.Values{"force": {boolQ(v.Force)}, "v": {boolQ(v.Volumes)}}
	if _, err := d.do(ctx, http.MethodDelete, "/containers/"+esc(v.ID), q); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// --- images ---

// imagePath is an image's path segment. References contain "/" and ":"
// (which the Engine's route takes as they are); the validator admits no
// other separator, no "%", "?", "#" and no "..".
func imagePath(p json.RawMessage) (string, error) {
	var v idParams
	if err := decodeParams(p, &v); err != nil {
		return "", err
	}
	if !ValidImage(v.ID) {
		return "", badParam("id must be an image ID or reference")
	}
	return "/images/" + v.ID, nil
}

func (d *Docker) imageInspect(ctx context.Context, p json.RawMessage) (any, error) {
	path, err := imagePath(p)
	if err != nil {
		return nil, err
	}
	raw, err := d.get(ctx, path+"/json", nil)
	if err != nil {
		return nil, err
	}
	return maskEnvIn(raw, "Config", "ContainerConfig")
}

func (d *Docker) imageHistory(ctx context.Context, p json.RawMessage) (any, error) {
	path, err := imagePath(p)
	if err != nil {
		return nil, err
	}
	return d.get(ctx, path+"/history", nil)
}

func (d *Docker) imageRemove(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		ID    string `json:"id"`
		Force bool   `json:"force"` // also when containers use it
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !ValidImage(v.ID) {
		return nil, badParam("id must be an image ID or reference")
	}
	b, err := d.do(ctx, http.MethodDelete, "/images/"+v.ID, url.Values{"force": {boolQ(v.Force)}})
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, Errorf(CodeFailed, "docker: answer is not JSON")
	}
	return json.RawMessage(b), nil
}

func (d *Docker) imageTag(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		ID   string `json:"id"`
		Repo string `json:"repo"`
		Tag  string `json:"tag"`
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	switch {
	case !ValidImage(v.ID):
		return nil, badParam("id must be an image ID or reference")
	case !ValidImageName(v.Repo):
		return nil, badParam("repo must be an image name without tag")
	case !ValidImageTag(v.Tag):
		return nil, badParam("tag is not a valid tag")
	}
	if _, err := d.do(ctx, http.MethodPost, "/images/"+v.ID+"/tag", url.Values{"repo": {v.Repo}, "tag": {v.Tag}}); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// PullResult is docker.images.pull's result.
type PullResult struct {
	Status string `json:"status"` // Docker's last status line
}

// imagePull pulls one tag (or digest) from its registry, without
// credentials. A reference without either pulls :latest, never every
// tag.
func (d *Docker) imagePull(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		Ref string `json:"ref"`
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	name, tag, digest, ok := ParseImageRef(v.Ref)
	if !ok {
		return nil, badParam("ref must be an image reference")
	}
	switch {
	case digest != "":
		tag = digest
	case tag == "":
		tag = "latest"
	}
	resp, err := d.send(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {name}, "tag": {tag}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	// The answer is a stream of JSON progress messages; an error arrives
	// as one of them, with status 200.
	dec := json.NewDecoder(io.LimitReader(resp.Body, 256<<20))
	var last string
	for {
		var m struct {
			Status string
			Error  string
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, Errorf(CodeFailed, "docker: pull interrupted")
		}
		if m.Error != "" {
			return nil, Errorf(CodeFailed, "docker: %s", truncate(m.Error, 300))
		}
		if m.Status != "" {
			last = m.Status
		}
	}
	return PullResult{Status: truncate(last, 300)}, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "?")
}

// --- networks and volumes ---

// predefinedNetworks cannot be removed.
var predefinedNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

func networkID(p json.RawMessage) (string, error) {
	var v idParams
	if err := decodeParams(p, &v); err != nil {
		return "", err
	}
	if !ValidNetwork(v.ID) {
		return "", badParam("id must be a network ID or name")
	}
	return v.ID, nil
}

func (d *Docker) networkInspect(ctx context.Context, p json.RawMessage) (any, error) {
	id, err := networkID(p)
	if err != nil {
		return nil, err
	}
	return d.get(ctx, "/networks/"+esc(id), nil)
}

func (d *Docker) networkRemove(ctx context.Context, p json.RawMessage) (any, error) {
	id, err := networkID(p)
	if err != nil {
		return nil, err
	}
	if predefinedNetworks[id] {
		return nil, badParam("%s is a predefined network", id)
	}
	if _, err := d.do(ctx, http.MethodDelete, "/networks/"+esc(id), nil); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func volumeName(p json.RawMessage) (string, bool, error) {
	var v struct {
		Name  string `json:"name"`
		Force bool   `json:"force"`
	}
	if err := decodeParams(p, &v); err != nil {
		return "", false, err
	}
	if !ValidVolume(v.Name) {
		return "", false, badParam("name must be a volume name")
	}
	return v.Name, v.Force, nil
}

func (d *Docker) volumeInspect(ctx context.Context, p json.RawMessage) (any, error) {
	name, force, err := volumeName(p)
	if err != nil {
		return nil, err
	}
	if force {
		return nil, badParam("inspect takes no force")
	}
	return d.get(ctx, "/volumes/"+esc(name), nil)
}

func (d *Docker) volumeRemove(ctx context.Context, p json.RawMessage) (any, error) {
	name, force, err := volumeName(p)
	if err != nil {
		return nil, err
	}
	if _, err := d.do(ctx, http.MethodDelete, "/volumes/"+esc(name), url.Values{"force": {boolQ(force)}}); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// --- cleanup ---

// Prune kinds.
var pruneKinds = map[string]bool{"containers": true, "images": true, "volumes": true, "networks": true, "build": true}

// PruneItem is one thing a prune would remove.
type PruneItem struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size"` // bytes; -1 when Docker does not know
}

// PrunePreview is docker.prune.preview's result. Until goes back to
// docker.prune, which removes nothing created after the preview.
type PrunePreview struct {
	Kind        string      `json:"kind"`
	Until       int64       `json:"until"` // Unix seconds
	Items       []PruneItem `json:"items"`
	Reclaimable int64       `json:"reclaimable"` // bytes
}

func pruneKind(k string) error {
	if !pruneKinds[k] {
		return badParam("kind must be containers, images, volumes, networks or build")
	}
	return nil
}

func (d *Docker) prunePreview(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		Kind string `json:"kind"`
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if err := pruneKind(v.Kind); err != nil {
		return nil, err
	}
	pv := PrunePreview{Kind: v.Kind, Until: d.now().Unix()}
	items, err := d.pruneCandidates(ctx, v.Kind)
	if err != nil {
		return nil, err
	}
	pv.Items = items
	for _, it := range items {
		if it.Size > 0 {
			pv.Reclaimable += it.Size
		}
	}
	return pv, nil
}

// pruneCandidates lists what a prune of kind would remove now.
func (d *Docker) pruneCandidates(ctx context.Context, kind string) ([]PruneItem, error) {
	items := []PruneItem{}
	switch kind {
	case "containers":
		var cs []struct {
			ID     string `json:"Id"`
			Names  []string
			SizeRw int64
		}
		q := url.Values{"all": {"true"}, "size": {"true"}, "filters": {filtersQ(map[string][]string{"status": {"created", "exited", "dead"}})}}
		if err := d.getInto(ctx, "/containers/json", q, &cs); err != nil {
			return nil, err
		}
		for _, c := range cs {
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			items = append(items, PruneItem{ID: c.ID, Name: name, Size: c.SizeRw})
		}
	case "images":
		var is []struct {
			ID   string `json:"Id"`
			Size int64
		}
		q := url.Values{"filters": {filtersQ(map[string][]string{"dangling": {"true"}})}}
		if err := d.getInto(ctx, "/images/json", q, &is); err != nil {
			return nil, err
		}
		for _, i := range is {
			items = append(items, PruneItem{ID: i.ID, Size: i.Size})
		}
	case "volumes":
		vs, err := d.unusedVolumes(ctx)
		if err != nil {
			return nil, err
		}
		items = append(items, vs...)
	case "networks":
		var ns []struct {
			ID   string `json:"Id"`
			Name string
		}
		if err := d.getInto(ctx, "/networks", nil, &ns); err != nil {
			return nil, err
		}
		// Network lists carry no containers: collect the networks every
		// container (running or not) is attached to.
		var cs []struct {
			NetworkSettings struct {
				Networks map[string]struct{ NetworkID string }
			}
		}
		if err := d.getInto(ctx, "/containers/json", url.Values{"all": {"true"}}, &cs); err != nil {
			return nil, err
		}
		used := map[string]bool{}
		for _, c := range cs {
			for _, n := range c.NetworkSettings.Networks {
				used[n.NetworkID] = true
			}
		}
		for _, n := range ns {
			if !predefinedNetworks[n.Name] && !used[n.ID] {
				items = append(items, PruneItem{ID: n.ID, Name: n.Name})
			}
		}
	case "build":
		var df struct {
			BuildCache []struct {
				ID     string
				Type   string
				InUse  bool
				Shared bool
				Size   int64
			}
		}
		if err := d.getInto(ctx, "/system/df", url.Values{"type": {"build-cache"}}, &df); err != nil {
			return nil, err
		}
		for _, b := range df.BuildCache {
			if !b.InUse && !b.Shared {
				items = append(items, PruneItem{ID: b.ID, Name: b.Type, Size: b.Size})
			}
		}
	}
	return items, nil
}

// unusedVolumes lists volumes no container references.
func (d *Docker) unusedVolumes(ctx context.Context) ([]PruneItem, error) {
	var df struct {
		Volumes []struct {
			Name      string
			UsageData *struct{ RefCount, Size int64 }
		}
	}
	if err := d.getInto(ctx, "/system/df", url.Values{"type": {"volume"}}, &df); err != nil {
		return nil, err
	}
	var out []PruneItem
	for _, v := range df.Volumes {
		if v.UsageData != nil && v.UsageData.RefCount == 0 {
			out = append(out, PruneItem{ID: v.Name, Name: v.Name, Size: v.UsageData.Size})
		}
	}
	return out, nil
}

// PruneResult is docker.prune's result.
type PruneResult struct {
	Deleted   []string `json:"deleted"`
	Reclaimed int64    `json:"reclaimed"`         // bytes
	Skipped   []string `json:"skipped,omitempty"` // volumes no longer unused
}

// prune removes what a preview listed, never anything newer: Docker's
// prune endpoints get until=<preview time>. Volumes have no until
// filter, so they are removed one by one, by name, from the preview's
// list, and only those still unused.
func (d *Docker) prune(ctx context.Context, p json.RawMessage) (any, error) {
	var v struct {
		Kind    string   `json:"kind"`
		Until   int64    `json:"until"`
		Volumes []string `json:"volumes"`
	}
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if err := pruneKind(v.Kind); err != nil {
		return nil, err
	}
	now := d.now().Unix()
	if v.Until <= 0 || v.Until > now || v.Until < now-24*3600 {
		return nil, badParam("until must be the time of a preview from the last 24 hours")
	}
	if v.Kind == "volumes" {
		return d.pruneVolumes(ctx, v.Volumes)
	}
	if v.Volumes != nil {
		return nil, badParam("volumes is only for kind volumes")
	}
	until := strconv.FormatInt(v.Until, 10)
	var path string
	filters := map[string][]string{"until": {until}}
	switch v.Kind {
	case "containers":
		path = "/containers/prune"
	case "images":
		path = "/images/prune"
		filters["dangling"] = []string{"true"}
	case "networks":
		path = "/networks/prune"
	case "build":
		// The builder's until is a duration (unused for longer than).
		path = "/build/prune"
		filters["until"] = []string{fmt.Sprintf("%ds", max(now-v.Until, 1))}
	}
	b, err := d.do(ctx, http.MethodPost, path, url.Values{"filters": {filtersQ(filters)}})
	if err != nil {
		return nil, err
	}
	var r struct {
		ContainersDeleted []string
		NetworksDeleted   []string
		CachesDeleted     []string
		ImagesDeleted     []struct{ Untagged, Deleted string }
		SpaceReclaimed    int64
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, Errorf(CodeFailed, "docker: unexpected answer")
	}
	out := PruneResult{Deleted: []string{}, Reclaimed: r.SpaceReclaimed}
	out.Deleted = append(out.Deleted, r.ContainersDeleted...)
	out.Deleted = append(out.Deleted, r.NetworksDeleted...)
	out.Deleted = append(out.Deleted, r.CachesDeleted...)
	for _, i := range r.ImagesDeleted {
		if i.Deleted != "" {
			out.Deleted = append(out.Deleted, i.Deleted)
		}
	}
	return out, nil
}

func (d *Docker) pruneVolumes(ctx context.Context, names []string) (any, error) {
	if len(names) == 0 || len(names) > 1000 {
		return nil, badParam("volumes must list 1–1000 volume names from the preview")
	}
	for _, n := range names {
		if !ValidVolume(n) {
			return nil, badParam("volumes must be volume names")
		}
	}
	unused, err := d.unusedVolumes(ctx)
	if err != nil {
		return nil, err
	}
	size := map[string]int64{}
	for _, u := range unused {
		size[u.Name] = u.Size
	}
	out := PruneResult{Deleted: []string{}}
	for _, n := range names {
		sz, ok := size[n]
		if !ok {
			out.Skipped = append(out.Skipped, n)
			continue
		}
		if _, err := d.do(ctx, http.MethodDelete, "/volumes/"+esc(n), nil); err != nil {
			var be *Error
			if errors.As(err, &be) && (be.Code == CodeConflict || be.Code == CodeNotFound) {
				out.Skipped = append(out.Skipped, n)
				continue
			}
			return nil, err
		}
		out.Deleted = append(out.Deleted, n)
		if sz > 0 {
			out.Reclaimed += sz
		}
		delete(size, n) // a name listed twice is removed once
	}
	return out, nil
}
