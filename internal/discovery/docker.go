package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// DockerTopic is the WebSocket topic for Docker objects. While it has
// subscribers the watcher follows the Engine's events through the
// broker; without them nothing runs, so the broker can exit when idle
// (SECURITY S11).
const DockerTopic = "docker"

// Docker states, for the UI's empty states.
const (
	DockerOK           = "ok"
	DockerNoBroker     = "no-broker"  // hearth-broker is not installed or not running
	DockerNotMember    = "not-member" // the user is not in the hearth group
	DockerNotAvailable = "no-docker"  // the Engine is not installed or not running
	DockerError        = "error"
)

// Compose labels.
const (
	labelProject    = "com.docker.compose.project"
	labelService    = "com.docker.compose.service"
	labelWorkingDir = "com.docker.compose.project.working_dir"
	labelConfigs    = "com.docker.compose.project.config_files"
)

// Container is one container as the lists show it; details come from
// docker.containers.inspect.
type Container struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	ImageID  string   `json:"image_id"`
	Command  string   `json:"command"`
	Created  int64    `json:"created"` // Unix s
	State    string   `json:"state"`   // created, running, paused, restarting, exited, dead
	Status   string   `json:"status"`  // Docker's text, e.g. "Up 2 hours (healthy)"
	Health   string   `json:"health,omitempty"`
	Ports    []Port   `json:"ports"`
	Networks []string `json:"networks"`
	Mounts   []Mount  `json:"mounts"`
	Project  string   `json:"project,omitempty"` // compose project
	Service  string   `json:"service,omitempty"` // compose service
}

// Port is a container port, published when Public is set.
type Port struct {
	IP      string `json:"ip,omitempty"`
	Private uint16 `json:"private"`
	Public  uint16 `json:"public,omitempty"`
	Type    string `json:"type"`
}

// Mount is a container mount.
type Mount struct {
	Type        string `json:"type"` // bind, volume, tmpfs, ...
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

// Image is one local image.
type Image struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Digests    []string `json:"digests"`
	Created    int64    `json:"created"` // Unix s
	Size       int64    `json:"size"`
	Dangling   bool     `json:"dangling"`
	Containers []string `json:"containers"` // IDs of containers using it
}

// Network is one network.
type Network struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope"`
	Internal   bool     `json:"internal"`
	Created    string   `json:"created"`
	Project    string   `json:"project,omitempty"`
	Containers []string `json:"containers"`
}

// Volume is one volume.
type Volume struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Mountpoint string   `json:"mountpoint"`
	Created    string   `json:"created"`
	Project    string   `json:"project,omitempty"`
	Containers []string `json:"containers"`
}

// Project is a compose project: the containers sharing its label.
type Project struct {
	Name        string   `json:"name"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files"`
	Services    []string `json:"services"`
	Containers  []string `json:"containers"`
	Running     int      `json:"running"`
}

// DockerSnapshot is everything the Docker pages list.
type DockerSnapshot struct {
	Kind       string      `json:"kind"`              // "snapshot" on DockerTopic
	State      string      `json:"state"`             // Docker* constants
	Error      string      `json:"error,omitempty"`   // for DockerError
	Version    string      `json:"version,omitempty"` // Engine version
	Time       int64       `json:"time"`              // Unix ms
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Networks   []Network   `json:"networks"`
	Volumes    []Volume    `json:"volumes"`
	Projects   []Project   `json:"projects"`
}

// DockerEventMsg is one Engine event on DockerTopic; a snapshot follows
// once a burst of them settles.
type DockerEventMsg struct {
	Kind  string             `json:"kind"` // "event"
	Event broker.DockerEvent `json:"event"`
}

// DockerBroker is the subset of broker.Client the watcher needs.
type DockerBroker interface {
	Call(ctx context.Context, method string, params, out any) error
	Stream(ctx context.Context, method string, params any, fn func(json.RawMessage) error) error
}

// TopicPublisher is the subset of api.Hub the watcher needs.
type TopicPublisher interface {
	Publish(topic string, data any) error
	Subscribers(topic string) int
	Watch(topic string) <-chan struct{}
}

// DockerConfig configures a DockerWatcher.
type DockerConfig struct {
	Broker   DockerBroker
	Pub      TopicPublisher
	Log      *slog.Logger
	Debounce time.Duration // default 500 ms (ARCHITECTURE "Discovery")
	Retry    time.Duration // after a lost stream; default 5 s
	Timeout  time.Duration // for one snapshot; default 20 s
}

// DockerWatcher lists Docker objects and follows their changes.
type DockerWatcher struct {
	cfg DockerConfig

	mu   sync.Mutex
	last *DockerSnapshot // current while a session runs, else nil
}

// NewDockerWatcher returns a watcher; call Run.
func NewDockerWatcher(cfg DockerConfig) *DockerWatcher {
	if cfg.Debounce == 0 {
		cfg.Debounce = 500 * time.Millisecond
	}
	if cfg.Retry == 0 {
		cfg.Retry = 5 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &DockerWatcher{cfg: cfg}
}

// Run follows Docker while DockerTopic has subscribers, until ctx ends.
func (w *DockerWatcher) Run(ctx context.Context) {
	watch := w.cfg.Pub.Watch(DockerTopic)
	for ctx.Err() == nil {
		if w.cfg.Pub.Subscribers(DockerTopic) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-watch:
			}
			continue
		}
		if w.session(ctx, watch) && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(w.cfg.Retry):
			}
		}
	}
}

// session publishes a snapshot, then one after each burst of events. It
// returns when the subscribers are gone (false) or the stream ends (true:
// the broker or Docker went away; retry later).
func (w *DockerWatcher) session(ctx context.Context, watch <-chan struct{}) (lost bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer w.setLast(nil)

	events := make(chan broker.DockerEvent, 64)
	ended := make(chan error, 1)
	go func() {
		ended <- w.cfg.Broker.Stream(ctx, "docker.events", nil, func(raw json.RawMessage) error {
			var e broker.DockerEvent
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			select {
			case events <- e:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	// The stream is requested first, so a change during the snapshot
	// usually brings another one.
	w.publish(ctx)

	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return false
		case <-watch:
			if w.cfg.Pub.Subscribers(DockerTopic) == 0 {
				return false
			}
			if s := w.getLast(); s != nil {
				_ = w.cfg.Pub.Publish(DockerTopic, s) // for the new subscriber
			}
		case e := <-events:
			_ = w.cfg.Pub.Publish(DockerTopic, DockerEventMsg{Kind: "event", Event: e})
			if settle == nil {
				settle = time.After(w.cfg.Debounce)
			}
		case <-settle:
			settle = nil
			w.publish(ctx)
		case err := <-ended:
			if ctx.Err() == nil {
				if s := w.getLast(); s == nil || s.State == DockerOK {
					w.cfg.Log.Warn("docker: lost the event stream", "err", err)
					w.publish(ctx) // the state may say why
				}
			}
			return true
		}
	}
}

func (w *DockerWatcher) publish(ctx context.Context) {
	s := w.Snapshot(ctx)
	if ctx.Err() != nil {
		return
	}
	w.setLast(&s)
	_ = w.cfg.Pub.Publish(DockerTopic, s)
}

func (w *DockerWatcher) setLast(s *DockerSnapshot) {
	w.mu.Lock()
	w.last = s
	w.mu.Unlock()
}

func (w *DockerWatcher) getLast() *DockerSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// HandleSnapshot serves GET /api/v1/docker: the watcher's current
// snapshot while it runs, else a fresh one.
func (w *DockerWatcher) HandleSnapshot(rw http.ResponseWriter, r *http.Request) {
	s := w.getLast()
	if s == nil {
		fresh := w.Snapshot(r.Context())
		s = &fresh
	}
	rw.Header().Set("Cache-Control", "no-store")
	writeJSON(rw, s)
}

// Raw shapes of the Engine's list answers (API v1.44).
type (
	rawContainer struct {
		ID      string `json:"Id"`
		Names   []string
		Image   string
		ImageID string
		Command string
		Created int64
		Ports   []struct {
			IP          string
			PrivatePort uint16
			PublicPort  uint16
			Type        string
		}
		Labels          map[string]string
		State           string
		Status          string
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
		Mounts []struct {
			Type        string
			Name        string
			Source      string
			Destination string
			RW          bool
		}
	}
	rawImage struct {
		ID          string `json:"Id"`
		RepoTags    []string
		RepoDigests []string
		Created     int64
		Size        int64
	}
	rawNetwork struct {
		ID       string `json:"Id"`
		Name     string
		Driver   string
		Scope    string
		Internal bool
		Created  string
		Labels   map[string]string
	}
	rawVolumes struct {
		Volumes []struct {
			Name       string
			Driver     string
			Mountpoint string
			CreatedAt  string
			Labels     map[string]string
		}
	}
)

// Snapshot lists every Docker object through the broker.
func (w *DockerWatcher) Snapshot(ctx context.Context) DockerSnapshot {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	s := DockerSnapshot{Kind: "snapshot", State: DockerOK}
	var (
		ver  struct{ Version string }
		cs   []rawContainer
		is   []rawImage
		ns   []rawNetwork
		vs   rawVolumes
		bErr error
	)
	call := func(method string, params, out any) {
		if bErr == nil {
			bErr = w.cfg.Broker.Call(ctx, method, params, out)
		}
	}
	call("docker.version", nil, &ver)
	call("docker.containers.list", map[string]bool{"all": true}, &cs)
	call("docker.images.list", nil, &is)
	call("docker.networks.list", nil, &ns)
	call("docker.volumes.list", nil, &vs)
	s.Time = time.Now().UnixMilli()
	if bErr != nil {
		s.State, s.Error = dockerState(bErr)
		s.Containers, s.Images, s.Networks, s.Volumes, s.Projects = []Container{}, []Image{}, []Network{}, []Volume{}, []Project{}
		return s
	}
	s.Version = ver.Version
	s.Containers = containers(cs)
	s.Images = images(is, s.Containers)
	s.Networks = networks(ns, cs)
	s.Volumes = volumes(vs, s.Containers)
	s.Projects = projects(cs)
	return s
}

func dockerState(err error) (state, msg string) {
	switch {
	case errors.Is(err, broker.ErrUnavailable):
		return DockerNoBroker, ""
	case errors.Is(err, broker.ErrNotMember):
		return DockerNotMember, ""
	case errors.Is(err, broker.ErrNoDocker):
		return DockerNotAvailable, ""
	}
	var be *broker.Error
	if errors.As(err, &be) {
		return DockerError, be.Message
	}
	return DockerError, "unexpected answer from the broker"
}

func containers(raw []rawContainer) []Container {
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{
			ID: r.ID, Image: r.Image, ImageID: r.ImageID, Command: r.Command, Created: r.Created,
			State: r.State, Status: r.Status, Health: health(r.Status),
			Ports: []Port{}, Networks: []string{}, Mounts: []Mount{},
			Project: r.Labels[labelProject], Service: r.Labels[labelService],
		}
		if len(r.Names) > 0 {
			c.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, p := range r.Ports {
			c.Ports = append(c.Ports, Port{IP: p.IP, Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
		}
		for n := range r.NetworkSettings.Networks {
			c.Networks = append(c.Networks, n)
		}
		sort.Strings(c.Networks)
		for _, m := range r.Mounts {
			c.Mounts = append(c.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// health reads a container's health from its status text; API v1.44
// lists have no health field.
func health(status string) string {
	switch {
	case strings.HasSuffix(status, "(healthy)"):
		return "healthy"
	case strings.HasSuffix(status, "(unhealthy)"):
		return "unhealthy"
	case strings.HasSuffix(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

func images(raw []rawImage, cs []Container) []Image {
	users := map[string][]string{}
	for _, c := range cs {
		users[c.ImageID] = append(users[c.ImageID], c.ID)
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		im := Image{ID: r.ID, Tags: nonNil(r.RepoTags), Digests: nonNil(r.RepoDigests), Created: r.Created, Size: r.Size, Containers: nonNil(users[r.ID])}
		// Docker lists an untagged image as "<none>:<none>" (or not at all).
		im.Tags = without(im.Tags, "<none>:<none>")
		im.Digests = without(im.Digests, "<none>@<none>")
		im.Dangling = len(im.Tags) == 0
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dangling != out[j].Dangling {
			return !out[i].Dangling
		}
		return out[i].Created > out[j].Created
	})
	return out
}

func networks(raw []rawNetwork, cs []rawContainer) []Network {
	users := map[string][]string{}
	for _, c := range cs {
		for _, n := range c.NetworkSettings.Networks {
			users[n.NetworkID] = append(users[n.NetworkID], c.ID)
		}
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		out = append(out, Network{
			ID: r.ID, Name: r.Name, Driver: r.Driver, Scope: r.Scope, Internal: r.Internal, Created: r.Created,
			Project: r.Labels[labelProject], Containers: nonNil(users[r.ID]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func volumes(raw rawVolumes, cs []Container) []Volume {
	users := map[string][]string{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				users[m.Name] = append(users[m.Name], c.ID)
			}
		}
	}
	out := make([]Volume, 0, len(raw.Volumes))
	for _, r := range raw.Volumes {
		out = append(out, Volume{
			Name: r.Name, Driver: r.Driver, Mountpoint: r.Mountpoint, Created: r.CreatedAt,
			Project: r.Labels[labelProject], Containers: nonNil(users[r.Name]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func projects(cs []rawContainer) []Project {
	byName := map[string]*Project{}
	for _, c := range cs {
		name := c.Labels[labelProject]
		if name == "" {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &Project{Name: name, ConfigFiles: []string{}, Services: []string{}, Containers: []string{}}
			byName[name] = p
		}
		if p.WorkingDir == "" {
			p.WorkingDir = c.Labels[labelWorkingDir]
		}
		if len(p.ConfigFiles) == 0 && c.Labels[labelConfigs] != "" {
			p.ConfigFiles = strings.Split(c.Labels[labelConfigs], ",")
		}
		if s := c.Labels[labelService]; s != "" && !slices.Contains(p.Services, s) {
			p.Services = append(p.Services, s)
		}
		p.Containers = append(p.Containers, c.ID)
		if c.State == "running" {
			p.Running++
		}
	}
	out := make([]Project, 0, len(byName))
	for _, p := range byName {
		sort.Strings(p.Services)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func without(s []string, drop string) []string {
	out := s[:0:0]
	for _, v := range s {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}
