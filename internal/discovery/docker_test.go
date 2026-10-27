package discovery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

const (
	dockerContainers = `[
	 {"Id":"c2","Names":["/db"],"Image":"postgres:16","ImageID":"sha256:pg","Command":"postgres","Created":20,
	  "Ports":[{"PrivatePort":5432,"Type":"tcp"}],
	  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db",
	            "com.docker.compose.project.working_dir":"/home/me/shop",
	            "com.docker.compose.project.config_files":"/home/me/shop/compose.yaml,/home/me/shop/compose.override.yaml"},
	  "State":"exited","Status":"Exited (0) 3 hours ago",
	  "NetworkSettings":{"Networks":{"shop_default":{"NetworkID":"n2"}}},
	  "Mounts":[{"Type":"volume","Name":"shop_data","Source":"/var/lib/docker/volumes/shop_data/_data","Destination":"/var/lib/postgresql/data","RW":true}]},
	 {"Id":"c1","Names":["/web"],"Image":"nginx","ImageID":"sha256:ngx","Command":"nginx","Created":10,
	  "Ports":[{"IP":"127.0.0.1","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],
	  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web"},
	  "State":"running","Status":"Up 2 hours (healthy)",
	  "NetworkSettings":{"Networks":{"shop_default":{"NetworkID":"n2"},"bridge":{"NetworkID":"n1"}}},
	  "Mounts":[{"Type":"bind","Source":"/home/me/site","Destination":"/usr/share/nginx/html","RW":false}]},
	 {"Id":"c3","Names":["/lonely"],"Image":"busybox","ImageID":"sha256:bb","State":"created","Status":"Created",
	  "NetworkSettings":{"Networks":{}},"Mounts":[]}
	]`
	dockerImages = `[
	 {"Id":"sha256:old","RepoTags":["<none>:<none>"],"RepoDigests":["<none>@<none>"],"Created":1,"Size":5},
	 {"Id":"sha256:ngx","RepoTags":["nginx:latest"],"RepoDigests":["nginx@sha256:abc"],"Created":100,"Size":190,"SharedSize":60},
	 {"Id":"sha256:pg","RepoTags":["postgres:16"],"RepoDigests":null,"Created":50,"Size":400}
	]`
	dockerNetworks = `[
	 {"Id":"n2","Name":"shop_default","Driver":"bridge","Scope":"local","Labels":{"com.docker.compose.project":"shop"}},
	 {"Id":"n1","Name":"bridge","Driver":"bridge","Scope":"local","Labels":{}},
	 {"Id":"n3","Name":"none","Driver":"null","Scope":"local"}
	]`
	dockerVolumes = `{"Volumes":[
	 {"Name":"shop_data","Driver":"local","Mountpoint":"/var/lib/docker/volumes/shop_data/_data","Labels":{"com.docker.compose.project":"shop"}},
	 {"Name":"orphan","Driver":"local","Mountpoint":"/var/lib/docker/volumes/orphan/_data","Labels":null}
	],"Warnings":null}`
)

// fakeDocker answers the broker's Docker methods.
type fakeDocker struct {
	mu      sync.Mutex
	err     error // for every call
	calls   int
	streams int
	events  chan string   // items of the current stream
	end     chan error    // ends the current stream
	open    chan struct{} // a stream started
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{events: make(chan string, 8), end: make(chan error, 1), open: make(chan struct{}, 8)}
}

func (f *fakeDocker) Call(_ context.Context, method string, params, out any) error {
	f.mu.Lock()
	f.calls++
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	answers := map[string]string{
		"docker.version":         `{"Version":"28.1.1"}`,
		"docker.containers.list": dockerContainers,
		"docker.images.list":     dockerImages,
		"docker.networks.list":   dockerNetworks,
		"docker.volumes.list":    dockerVolumes,
	}
	if method == "docker.containers.list" {
		if b, _ := json.Marshal(params); string(b) != `{"all":true}` {
			return broker.Errorf(broker.CodeBadParams, "want all, got %s", b)
		}
	}
	return json.Unmarshal([]byte(answers[method]), out)
}

func (f *fakeDocker) Stream(ctx context.Context, method string, _ any, fn func(json.RawMessage) error) error {
	if method != "docker.events" {
		return broker.ErrNoMethod
	}
	f.mu.Lock()
	f.streams++
	err := f.err
	f.mu.Unlock()
	select {
	case f.open <- struct{}{}:
	default:
	}
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-f.end:
			return err
		case e := <-f.events:
			if err := fn(json.RawMessage(e)); err != nil {
				return err
			}
		}
	}
}

func (f *fakeDocker) count() (calls, streams int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.streams
}

// topicHub records what is published on DockerTopic.
type topicHub struct {
	mu    sync.Mutex
	subs  int
	msgs  []json.RawMessage
	watch chan struct{}
}

func (h *topicHub) Publish(topic string, v any) error {
	if topic != DockerTopic {
		panic(topic)
	}
	b, err := json.Marshal(v)
	h.mu.Lock()
	h.msgs = append(h.msgs, b)
	h.mu.Unlock()
	return err
}

func (h *topicHub) Subscribers(string) int { h.mu.Lock(); defer h.mu.Unlock(); return h.subs }

func (h *topicHub) Watch(string) <-chan struct{} { return h.watch }

func (h *topicHub) setSubs(n int) {
	h.mu.Lock()
	h.subs = n
	h.mu.Unlock()
	select {
	case h.watch <- struct{}{}:
	default:
	}
}

// kinds lists the published messages' kinds, events as "event:<action>".
func (h *topicHub) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.msgs {
		var v struct {
			Kind  string
			State string
			Event struct{ Action string }
		}
		_ = json.Unmarshal(m, &v)
		switch v.Kind {
		case "event":
			out = append(out, "event:"+v.Event.Action)
		default:
			out = append(out, v.Kind+":"+v.State)
		}
	}
	return out
}

func newTestWatcher(f *fakeDocker, hub *topicHub) *DockerWatcher {
	return NewDockerWatcher(DockerConfig{
		Broker: f, Pub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Debounce: 20 * time.Millisecond, Retry: 30 * time.Millisecond,
	})
}

func TestDockerSnapshot(t *testing.T) {
	w := newTestWatcher(newFakeDocker(), &topicHub{watch: make(chan struct{}, 1)})
	s := w.Snapshot(context.Background())
	if s.State != DockerOK || s.Version != "28.1.1" || s.Kind != "snapshot" {
		t.Fatalf("%+v", s)
	}
	names := func(n int, name func(int) string) []string {
		var out []string
		for i := range n {
			out = append(out, name(i))
		}
		return out
	}
	if got := names(len(s.Containers), func(i int) string { return s.Containers[i].Name }); !slices.Equal(got, []string{"db", "lonely", "web"}) {
		t.Fatalf("containers %v", got)
	}
	web := s.Containers[2]
	if web.Health != "healthy" || web.Project != "shop" || web.Service != "web" ||
		!slices.Equal(web.Networks, []string{"bridge", "shop_default"}) ||
		len(web.Ports) != 1 || web.Ports[0] != (Port{IP: "127.0.0.1", Private: 80, Public: 8080, Type: "tcp"}) ||
		len(web.Mounts) != 1 || web.Mounts[0].RW {
		t.Fatalf("web %+v", web)
	}
	if lonely := s.Containers[1]; lonely.Health != "" || lonely.Ports == nil || lonely.Mounts == nil || lonely.Networks == nil {
		t.Fatalf("lonely %+v", lonely)
	}

	// Images: tagged newest first, dangling last; used-by from ImageID.
	if got := names(len(s.Images), func(i int) string { return s.Images[i].ID }); !slices.Equal(got, []string{"sha256:ngx", "sha256:pg", "sha256:old"}) {
		t.Fatalf("images %v", got)
	}
	if old := s.Images[2]; !old.Dangling || len(old.Tags) != 0 || len(old.Digests) != 0 || old.Containers == nil {
		t.Fatalf("dangling %+v", old)
	}
	if ngx := s.Images[0]; ngx.SharedSize != 60 {
		t.Fatalf("shared size %+v", ngx)
	}
	if pg := s.Images[1]; !slices.Equal(pg.Containers, []string{"c2"}) || pg.Digests == nil {
		t.Fatalf("pg %+v", pg)
	}

	if got := names(len(s.Networks), func(i int) string { return s.Networks[i].Name }); !slices.Equal(got, []string{"bridge", "none", "shop_default"}) {
		t.Fatalf("networks %v", got)
	}
	if n := s.Networks[2]; n.Project != "shop" || !slices.Equal(n.Containers, []string{"c2", "c1"}) {
		t.Fatalf("shop network %+v", n)
	}
	if n := s.Networks[1]; n.Containers == nil || len(n.Containers) != 0 {
		t.Fatalf("none network %+v", n)
	}

	if len(s.Volumes) != 2 || s.Volumes[1].Name != "shop_data" || !slices.Equal(s.Volumes[1].Containers, []string{"c2"}) || len(s.Volumes[0].Containers) != 0 {
		t.Fatalf("volumes %+v", s.Volumes)
	}

	if len(s.Projects) != 1 {
		t.Fatalf("projects %+v", s.Projects)
	}
	p := s.Projects[0]
	if p.Name != "shop" || p.WorkingDir != "/home/me/shop" || p.Running != 1 ||
		!slices.Equal(p.Services, []string{"db", "web"}) || !slices.Equal(p.Containers, []string{"c2", "c1"}) ||
		!slices.Equal(p.ConfigFiles, []string{"/home/me/shop/compose.yaml", "/home/me/shop/compose.override.yaml"}) {
		t.Fatalf("project %+v", p)
	}
}

func TestDockerSnapshotStates(t *testing.T) {
	for _, tc := range []struct {
		err         error
		state, text string
	}{
		{broker.ErrUnavailable, DockerNoBroker, ""},
		{broker.ErrNotMember, DockerNotMember, ""},
		{broker.ErrNoDocker, DockerNotAvailable, ""},
		{broker.Errorf(broker.CodeFailed, "docker: timed out"), DockerError, "docker: timed out"},
	} {
		f := newFakeDocker()
		f.err = tc.err
		s := newTestWatcher(f, &topicHub{}).Snapshot(context.Background())
		if s.State != tc.state || s.Error != tc.text || s.Containers == nil || s.Projects == nil {
			t.Errorf("%v: %+v", tc.err, s)
		}
		// One failed call is enough.
		if calls, _ := f.count(); calls != 1 {
			t.Errorf("%v: %d calls", tc.err, calls)
		}
		b, _ := json.Marshal(s)
		if !strings.Contains(string(b), `"containers":[]`) {
			t.Errorf("%s", b)
		}
	}
}

func TestDockerWatcher(t *testing.T) {
	f := newFakeDocker()
	hub := &topicHub{watch: make(chan struct{}, 1)}
	w := newTestWatcher(f, hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Without subscribers the broker is left alone.
	time.Sleep(40 * time.Millisecond)
	if calls, streams := f.count(); calls != 0 || streams != 0 {
		t.Fatalf("worked while idle: %d calls, %d streams", calls, streams)
	}

	hub.setSubs(1)
	<-f.open
	waitFor(t, "first snapshot", func() bool { return slices.Equal(hub.kinds(), []string{"snapshot:ok"}) })

	// REST serves the cached snapshot while the watcher runs.
	calls, _ := f.count()
	rec := httptest.NewRecorder()
	w.HandleSnapshot(rec, httptest.NewRequest("GET", "/api/v1/docker", nil))
	if now, _ := f.count(); now != calls || !strings.Contains(rec.Body.String(), `"name":"shop"`) {
		t.Fatalf("REST: %d calls, %s", now-calls, rec.Body.String())
	}

	// A burst of events is forwarded and brings one snapshot.
	for _, a := range []string{"die", "stop", "destroy"} {
		f.events <- `{"type":"container","action":"` + a + `","id":"c3","name":"lonely","time":1}`
	}
	want := []string{"snapshot:ok", "event:die", "event:stop", "event:destroy", "snapshot:ok"}
	waitFor(t, "events and one snapshot", func() bool { return slices.Equal(hub.kinds(), want) })
	time.Sleep(50 * time.Millisecond)
	if got := hub.kinds(); len(got) != len(want) {
		t.Fatalf("extra messages: %v", got)
	}

	// A second client gets the snapshot again.
	hub.setSubs(2)
	waitFor(t, "snapshot for new client", func() bool { return len(hub.kinds()) == len(want)+1 })

	// A lost stream: a snapshot that says why, then a retry.
	f.mu.Lock()
	f.err = broker.ErrUnavailable
	f.mu.Unlock()
	f.end <- broker.ErrUnavailable
	waitFor(t, "state after loss", func() bool { k := hub.kinds(); return k[len(k)-1] == "snapshot:no-broker" })
	<-f.open // the retry
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	waitFor(t, "recovered", func() bool { k := hub.kinds(); return k[len(k)-1] == "snapshot:ok" })

	// The last client leaves: the stream is closed, and REST lists afresh.
	hub.setSubs(0)
	waitFor(t, "no cache", func() bool { return w.getLast() == nil })
	calls, _ = f.count()
	rec = httptest.NewRecorder()
	w.HandleSnapshot(rec, httptest.NewRequest("GET", "/api/v1/docker", nil))
	if now, _ := f.count(); now != calls+5 {
		t.Fatalf("REST after session: %d calls", now-calls)
	}
	cancel()
	<-done
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
