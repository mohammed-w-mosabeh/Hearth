package broker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine is a Docker Engine on a Unix socket that records every
// request and answers from a table ("METHOD /path" → status, body).
type fakeEngine struct {
	mu      sync.Mutex
	reqs    []string
	answers map[string]answer
}

type answer struct {
	status int
	body   string
}

func newFakeEngine(t *testing.T, answers map[string]answer) (*fakeEngine, string) {
	t.Helper()
	f := &fakeEngine{answers: answers}
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/"+DockerAPI)
		f.mu.Lock()
		rec := key
		if r.URL.RawQuery != "" {
			q, _ := url.ParseQuery(r.URL.RawQuery)
			rec += "?" + q.Encode()
		}
		f.reqs = append(f.reqs, rec)
		a, ok := f.answers[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"page not found"}`))
			return
		}
		if a.status == 0 {
			a.status = http.StatusOK
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return f, sock
}

func (f *fakeEngine) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// dockerHarness serves the Docker methods against a fake engine.
func dockerHarness(t *testing.T, allow bool, answers map[string]answer) (*harness, *fakeEngine, *Docker) {
	t.Helper()
	f, sock := newFakeEngine(t, answers)
	d := NewDocker(sock)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(d)...) })
	h.auth.allow = allow
	return h, f, d
}

func call(t *testing.T, h *harness, method, params string, out any) error {
	t.Helper()
	var p any
	if params != "" {
		p = json.RawMessage(params)
	}
	return h.client().Call(context.Background(), method, p, out)
}

func code(err error) int {
	var be *Error
	if errors.As(err, &be) {
		return be.Code
	}
	return 0
}

const containerJSON = `{"Id":"abc","Config":{"Tty":false,"Env":["PATH=/bin","DB_PASSWORD=hunter2","API_KEY=k","USER=me"]},
 "HostConfig":{"Privileged":true,"NetworkMode":"host","Binds":["/etc:/host-etc:ro","data:/data"],"Mounts":null}}`

func TestDockerReadMethodsSendExactlyOneKnownRequest(t *testing.T) {
	ok := answer{body: `[]`}
	h, f, _ := dockerHarness(t, false, map[string]answer{
		"GET /version":                      ok,
		"GET /containers/json":              ok,
		"GET /containers/web/stats":         {body: `{}`},
		"GET /images/nginx:1.27/json":       {body: `{"Config":{"Env":["SECRET_X=1"]},"ContainerConfig":null}`},
		"GET /images/library/a/b:1/history": ok,
		"GET /networks/net1":                {body: `{}`},
		"GET /volumes/vol.1":                {body: `{}`},
	})
	cases := []struct{ method, params, want string }{
		{"docker.version", "", "GET /version"},
		{"docker.containers.list", `{"all":true}`, "GET /containers/json?all=true&size=false"},
		{"docker.containers.stats", `{"id":"web"}`, "GET /containers/web/stats?one-shot=true&stream=false"},
		{"docker.images.inspect", `{"id":"nginx:1.27"}`, "GET /images/nginx:1.27/json"},
		{"docker.images.history", `{"id":"library/a/b:1"}`, "GET /images/library/a/b:1/history"},
		{"docker.networks.inspect", `{"id":"net1"}`, "GET /networks/net1"},
		{"docker.volumes.inspect", `{"name":"vol.1"}`, "GET /volumes/vol.1"},
	}
	for _, c := range cases {
		var out json.RawMessage
		if err := call(t, h, c.method, c.params, &out); err != nil {
			t.Fatalf("%s: %v", c.method, err)
		}
		reqs := f.requests()
		if got := reqs[len(reqs)-1]; got != c.want {
			t.Errorf("%s sent %q, want %q", c.method, got, c.want)
		}
		if c.method == "docker.images.inspect" && strings.Contains(string(out), `SECRET_X=1`) {
			t.Errorf("image env not masked: %s", out)
		}
	}
	if len(f.requests()) != len(cases) {
		t.Fatalf("extra requests: %v", f.requests())
	}
	if len(h.auth.asked) != 0 {
		t.Fatalf("read methods asked polkit: %v", h.auth.asked)
	}
}

func TestDockerInspectMasksEnvAndReportsRisks(t *testing.T) {
	h, _, _ := dockerHarness(t, false, map[string]answer{"GET /containers/abc/json": {body: containerJSON}})
	var got ContainerInspect
	if err := call(t, h, "docker.containers.inspect", `{"id":"abc"}`, &got); err != nil {
		t.Fatal(err)
	}
	var in struct{ Config struct{ Env []string } }
	if err := json.Unmarshal(got.Inspect, &in); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "DB_PASSWORD=" + EnvMask, "API_KEY=" + EnvMask, "USER=me"}
	if !slices.Equal(in.Config.Env, want) {
		t.Fatalf("env %v", in.Config.Env)
	}
	if strings.Contains(string(got.Inspect), "hunter2") {
		t.Fatal("secret leaked")
	}
	if wantR := []string{RiskPrivileged, RiskHostNet, RiskHostMount + ":/etc"}; !slices.Equal(got.Risks, wantR) {
		t.Fatalf("risks %v", got.Risks)
	}
}

func TestDockerRejectsBadArguments(t *testing.T) {
	h, f, _ := dockerHarness(t, true, nil)
	cases := []struct{ method, params string }{
		{"docker.containers.inspect", `{"id":"../../version"}`},
		{"docker.containers.inspect", `{"id":"-f"}`},
		{"docker.containers.inspect", `{"id":"a/b"}`},
		{"docker.containers.inspect", `{"id":"a?x=1"}`},
		{"docker.containers.inspect", `{"id":"a%2fb"}`},
		{"docker.containers.inspect", `{"id":""}`},
		{"docker.containers.inspect", `{}`},
		{"docker.containers.inspect", `{"id":"web","extra":1}`},
		{"docker.containers.logs", `{"id":"web","tail":0}`},
		{"docker.containers.logs", `{"id":"web","tail":100000}`},
		{"docker.containers.start", `{"id":"web","timeout":5}`},
		{"docker.containers.stop", `{"id":"web","timeout":601}`},
		{"docker.images.inspect", `{"id":"../x"}`},
		{"docker.images.inspect", `{"id":"Nginx"}`},
		{"docker.images.inspect", `{"id":"nginx:latest#x"}`},
		{"docker.images.pull", `{"ref":"nginx:-x"}`},
		{"docker.images.pull", `{"ref":"https://evil/x"}`},
		{"docker.images.tag", `{"id":"nginx","repo":"x:1","tag":"2"}`},
		{"docker.images.tag", `{"id":"nginx","repo":"x","tag":"-2"}`},
		{"docker.networks.remove", `{"id":"bridge"}`},
		{"docker.networks.remove", `{"id":"host"}`},
		{"docker.volumes.remove", `{"name":"../etc"}`},
		{"docker.volumes.inspect", `{"name":"v","force":true}`},
		{"docker.prune.preview", `{"kind":"system"}`},
		{"docker.prune", `{"kind":"containers"}`},
		{"docker.prune", `{"kind":"containers","until":1800000100}`}, // in the future
		{"docker.prune", `{"kind":"containers","until":1700000000}`}, // stale preview
		{"docker.prune", `{"kind":"volumes","until":1800000000}`},    // no names
		{"docker.prune", `{"kind":"images","until":1800000000,"volumes":["a"]}`},
		{"docker.version", `{"x":1}`},
	}
	for _, c := range cases {
		if err := call(t, h, c.method, c.params, nil); code(err) != CodeBadParams {
			t.Errorf("%s %s: %v, want bad params", c.method, c.params, err)
		}
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Fatalf("bad arguments reached Docker: %v", reqs)
	}
}

// SECURITY S12 / Gate 2: nothing creates a container or reaches an
// arbitrary endpoint; the only exec is S12a's privileged shell session.
func TestDockerHasNoCreateExecOrPassthrough(t *testing.T) {
	h, f, d := dockerHarness(t, true, nil)
	for _, m := range []string{
		"docker.containers.create", "docker.exec", "docker.raw", "docker.request",
		"docker.containers.update", "docker.containers.archive", "docker.images.build", "docker.images.load",
		"docker.images.push", "docker.volumes.create", "docker.networks.connect", "docker.plugins.install",
		"docker.swarm.init", "docker.secrets.list",
	} {
		params := `{"Image":"alpine","HostConfig":{"Privileged":true,"Binds":["/:/host"]}}`
		if err := call(t, h, m, params, nil); !errors.Is(err, ErrNoMethod) {
			t.Errorf("%s: %v, want unknown method", m, err)
		}
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Fatalf("reached Docker: %v", reqs)
	}
	for _, n := range h.client().mustPing(t).Methods {
		if strings.Contains(n, "create") || (strings.Contains(n, "exec") && n != "docker.containers.exec") {
			t.Errorf("method %s exists", n)
		}
	}
	for _, m := range DockerMethods(d) {
		if strings.Contains(m.Name, "exec") && (!m.Privileged || m.Action != ActionDockerExec || m.Session == nil) {
			t.Errorf("%s: must be a privileged session under %s", m.Name, ActionDockerExec)
		}
	}
}

func (c *Client) mustPing(t *testing.T) PingResult {
	t.Helper()
	var p PingResult
	if err := c.Call(context.Background(), "broker.ping", nil, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDockerChangesNeedPolkit(t *testing.T) {
	h, f, _ := dockerHarness(t, false, nil)
	changes := map[string]string{
		"docker.containers.start":   `{"id":"web"}`,
		"docker.containers.stop":    `{"id":"web"}`,
		"docker.containers.restart": `{"id":"web"}`,
		"docker.containers.pause":   `{"id":"web"}`,
		"docker.containers.unpause": `{"id":"web"}`,
		"docker.containers.remove":  `{"id":"web"}`,
		"docker.images.remove":      `{"id":"nginx"}`,
		"docker.images.tag":         `{"id":"nginx","repo":"x","tag":"1"}`,
		"docker.images.pull":        `{"ref":"nginx"}`,
		"docker.networks.remove":    `{"id":"n"}`,
		"docker.volumes.remove":     `{"name":"v"}`,
		"docker.prune":              `{"kind":"containers","until":1800000000}`,
	}
	for m, p := range changes {
		if err := call(t, h, m, p, nil); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s: %v, want not authorized", m, err)
		}
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Fatalf("unauthorized calls reached Docker: %v", reqs)
	}
	var control, remove int
	for _, a := range h.auth.asked {
		switch {
		case strings.HasPrefix(a, ActionDockerControl+" "):
			control++
		case strings.HasPrefix(a, ActionDockerRemove+" "):
			remove++
		default:
			t.Errorf("asked %s", a)
		}
	}
	if control != 7 || remove != 5 {
		t.Fatalf("control %d remove %d: %v", control, remove, h.auth.asked)
	}
}

// Privileged calls with bad params are refused before polkit (no
// dialog), by a dry run of the handler that cannot reach the Engine.
func TestDockerBadParamsBeforePolkit(t *testing.T) {
	h, f, _ := dockerHarness(t, true, nil)
	for m, p := range map[string]string{
		"docker.containers.remove": `{"id":"../../containers/web"}`,
		"docker.containers.stop":   `{"id":"web","timeout":-1}`,
		"docker.containers.start":  `{"id":"web","timeout":5}`,
		"docker.images.remove":     `{"id":"a/../b"}`,
		"docker.images.tag":        `{"id":"nginx","repo":"../x","tag":"1"}`,
		"docker.prune":             `{"kind":"system","until":1800000000}`,
		"docker.volumes.remove":    `{"name":"../x"}`,
		"docker.containers.exec":   `{"id":"web","cmd":["/bin/bash"]}`,
	} {
		if err := call(t, h, m, p, nil); code(err) != CodeBadParams {
			t.Errorf("%s %s: %v, want bad params", m, p, err)
		}
	}
	if len(h.auth.asked) != 0 {
		t.Errorf("polkit asked: %v", h.auth.asked)
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Fatalf("reached Docker: %v", reqs)
	}
}

func TestDockerAuthorizedChanges(t *testing.T) {
	h, f, _ := dockerHarness(t, true, map[string]answer{
		"POST /containers/web/start":  {status: http.StatusNoContent},
		"POST /containers/web/stop":   {status: http.StatusNotModified}, // already stopped
		"DELETE /containers/web":      {status: http.StatusNoContent},
		"DELETE /images/nginx:1":      {body: `[{"Untagged":"nginx:1"}]`},
		"POST /images/nginx/tag":      {status: http.StatusCreated},
		"POST /images/create":         {body: `{"status":"Pulling"}` + "\n" + `{"status":"Status: Image is up to date for nginx:latest"}`},
		"DELETE /volumes/v":           {status: http.StatusConflict, body: `{"message":"volume is in use"}`},
		"POST /containers/gone/start": {status: http.StatusNotFound, body: `{"message":"No such container: gone"}`},
	})
	mustOK := func(m, p string, out any) {
		t.Helper()
		if err := call(t, h, m, p, out); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	mustOK("docker.containers.start", `{"id":"web"}`, nil)
	mustOK("docker.containers.stop", `{"id":"web","timeout":0}`, nil)
	mustOK("docker.containers.remove", `{"id":"web","force":true}`, nil)
	mustOK("docker.images.remove", `{"id":"nginx:1"}`, nil)
	mustOK("docker.images.tag", `{"id":"nginx","repo":"localhost:5000/me/nginx","tag":"v1"}`, nil)
	var pr PullResult
	mustOK("docker.images.pull", `{"ref":"nginx"}`, &pr)
	if !strings.Contains(pr.Status, "up to date") {
		t.Fatalf("pull %+v", pr)
	}
	if err := call(t, h, "docker.volumes.remove", `{"name":"v"}`, nil); code(err) != CodeConflict || !strings.Contains(err.Error(), "volume is in use") {
		t.Fatalf("in use: %v", err)
	}
	if err := call(t, h, "docker.containers.start", `{"id":"gone"}`, nil); code(err) != CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	want := []string{
		"POST /containers/web/start",
		"POST /containers/web/stop?t=0",
		"DELETE /containers/web?force=true&v=false",
		"DELETE /images/nginx:1?force=false",
		"POST /images/nginx/tag?repo=localhost%3A5000%2Fme%2Fnginx&tag=v1",
		"POST /images/create?fromImage=nginx&tag=latest", // never every tag
		"DELETE /volumes/v?force=false",
		"POST /containers/gone/start",
	}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests\n got %q\nwant %q", got, want)
	}
	if got := h.audit.results(); !slices.Contains(got, "docker.volumes.remove error: docker: volume is in use") {
		t.Fatalf("audit %v", got)
	}
}

func TestDockerPullErrorInStream(t *testing.T) {
	h, _, _ := dockerHarness(t, true, map[string]answer{
		"POST /images/create": {body: `{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"denied"},"error":"pull access denied"}`},
	})
	if err := call(t, h, "docker.images.pull", `{"ref":"ghcr.io/me/private@sha256:`+strings.Repeat("a", 64)+`"}`, nil); err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("%v", err)
	}
}

func TestDockerLogs(t *testing.T) {
	frame := func(stream byte, s string) string {
		h := make([]byte, 8)
		h[0] = stream
		binary.BigEndian.PutUint32(h[4:], uint32(len(s))) // #nosec G115 -- short test strings
		return string(h) + s
	}
	h, f, _ := dockerHarness(t, false, map[string]answer{
		"GET /containers/web/json": {body: `{"Config":{"Tty":false}}`},
		"GET /containers/web/logs": {body: frame(1, "hello\nworld\n") + frame(2, "oops\r\n") + "\x01\x00\x00\x00\x00\x00\x00\xffcut"},
		"GET /containers/tty/json": {body: `{"Config":{"Tty":true}}`},
		"GET /containers/tty/logs": {body: "a\r\nb\xff\n"},
	})
	var l Logs
	if err := call(t, h, "docker.containers.logs", `{"id":"web","tail":50}`, &l); err != nil {
		t.Fatal(err)
	}
	want := []LogLine{{"stdout", "hello"}, {"stdout", "world"}, {"stderr", "oops"}}
	if !slices.Equal(l.Lines, want) || l.Truncated {
		t.Fatalf("%+v", l)
	}
	if err := call(t, h, "docker.containers.logs", `{"id":"tty"}`, &l); err != nil {
		t.Fatal(err)
	}
	if want := []LogLine{{"stdout", "a"}, {"stdout", "b�"}}; !slices.Equal(l.Lines, want) {
		t.Fatalf("%+v", l)
	}
	if got := f.requests()[1]; got != "GET /containers/web/logs?since=0&stderr=true&stdout=true&tail=50&timestamps=false" {
		t.Fatalf("%s", got)
	}
}

func TestDockerPrunePreviewAndPrune(t *testing.T) {
	h, f, _ := dockerHarness(t, true, map[string]answer{
		"GET /containers/json":   {body: `[{"Id":"c1","Names":["/old"],"SizeRw":100,"NetworkSettings":{"Networks":{"x":{"NetworkID":"n-used"}}}}]`},
		"GET /system/df":         {body: `{"Volumes":[{"Name":"orphan","UsageData":{"RefCount":0,"Size":50}},{"Name":"busy","UsageData":{"RefCount":1,"Size":9}},{"Name":"nosize","UsageData":{"RefCount":0,"Size":-1}}],"BuildCache":[{"ID":"b1","Type":"regular","InUse":false,"Shared":false,"Size":7},{"ID":"b2","InUse":true,"Size":8}]}`},
		"GET /networks":          {body: `[{"Id":"n-bridge","Name":"bridge"},{"Id":"n-used","Name":"app_default"},{"Id":"n-free","Name":"old_default"}]`},
		"POST /containers/prune": {body: `{"ContainersDeleted":["c1"],"SpaceReclaimed":100}`},
		"POST /build/prune":      {body: `{"CachesDeleted":["b1"],"SpaceReclaimed":7}`},
		"DELETE /volumes/orphan": {status: http.StatusNoContent},
	})
	preview := func(kind string) PrunePreview {
		t.Helper()
		var pv PrunePreview
		if err := call(t, h, "docker.prune.preview", `{"kind":"`+kind+`"}`, &pv); err != nil {
			t.Fatal(err)
		}
		return pv
	}
	if pv := preview("containers"); pv.Until != 1_800_000_000 || pv.Reclaimable != 100 || len(pv.Items) != 1 || pv.Items[0].Name != "old" {
		t.Fatalf("%+v", pv)
	}
	if pv := preview("volumes"); pv.Reclaimable != 50 || len(pv.Items) != 2 {
		t.Fatalf("%+v", pv)
	}
	if pv := preview("networks"); len(pv.Items) != 1 || pv.Items[0].Name != "old_default" {
		t.Fatalf("%+v", pv)
	}
	if pv := preview("build"); pv.Reclaimable != 7 || len(pv.Items) != 1 {
		t.Fatalf("%+v", pv)
	}
	if len(h.auth.asked) != 0 {
		t.Fatalf("preview asked polkit")
	}

	var r PruneResult
	if err := call(t, h, "docker.prune", `{"kind":"containers","until":1799999990}`, &r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Deleted, []string{"c1"}) || r.Reclaimed != 100 {
		t.Fatalf("%+v", r)
	}
	if err := call(t, h, "docker.prune", `{"kind":"build","until":1799999990}`, &r); err != nil {
		t.Fatal(err)
	}
	// Volumes: only the previewed names that are still unused.
	if err := call(t, h, "docker.prune", `{"kind":"volumes","until":1800000000,"volumes":["orphan","busy","orphan"]}`, &r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Deleted, []string{"orphan"}) || !slices.Equal(r.Skipped, []string{"busy", "orphan"}) || r.Reclaimed != 50 {
		t.Fatalf("%+v", r)
	}
	reqs := f.requests()
	for _, want := range []string{
		`POST /containers/prune?filters=%7B%22until%22%3A%5B%221799999990%22%5D%7D`,
		`POST /build/prune?filters=%7B%22until%22%3A%5B%2210s%22%5D%7D`,
		`DELETE /volumes/orphan`,
	} {
		if !slices.Contains(reqs, want) {
			t.Errorf("missing %s in %q", want, reqs)
		}
	}
	for _, r := range reqs {
		if strings.Contains(r, "/volumes/busy") || strings.Contains(r, "/volumes/prune") {
			t.Errorf("sent %s", r)
		}
	}
}

func TestDockerUnavailable(t *testing.T) {
	d := NewDocker(filepath.Join(t.TempDir(), "none.sock"))
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(d)...) })
	if err := call(t, h, "docker.version", "", nil); !errors.Is(err, ErrNoDocker) {
		t.Fatalf("%v", err)
	}
}

func TestClientReadsLargeResponses(t *testing.T) {
	big := `["` + strings.Repeat("x", 1<<20) + `"]`
	h, _, _ := dockerHarness(t, false, map[string]answer{"GET /images/json": {body: big}})
	var out []string
	if err := call(t, h, "docker.images.list", "", &out); err != nil || len(out[0]) != 1<<20 {
		t.Fatalf("%v", err)
	}
}

// TestDockerLive runs the read methods against the real Engine:
// HEARTH_DOCKER_LIVE=1 go test -run TestDockerLive ./internal/broker/
// (needs access to /run/docker.sock). It changes nothing.
func TestDockerLive(t *testing.T) {
	if os.Getenv("HEARTH_DOCKER_LIVE") != "1" {
		t.Skip("set HEARTH_DOCKER_LIVE=1")
	}
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(NewDocker(DockerSocket))...) })
	var cs []struct {
		ID    string `json:"Id"`
		Names []string
	}
	if err := call(t, h, "docker.containers.list", `{"all":true}`, &cs); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"docker.version", "docker.info", "docker.df", "docker.images.list", "docker.networks.list", "docker.volumes.list"} {
		if err := call(t, h, m, "", nil); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	for _, k := range []string{"containers", "images", "volumes", "networks", "build"} {
		var pv PrunePreview
		if err := call(t, h, "docker.prune.preview", `{"kind":"`+k+`"}`, &pv); err != nil {
			t.Errorf("preview %s: %v", k, err)
		}
		t.Logf("prune %s: %d items, %d bytes", k, len(pv.Items), pv.Reclaimable)
	}
	if len(cs) == 0 {
		t.Log("no containers")
		return
	}
	id := cs[0].ID
	var in ContainerInspect
	if err := call(t, h, "docker.containers.inspect", `{"id":"`+id+`"}`, &in); err != nil {
		t.Fatal(err)
	}
	var l Logs
	if err := call(t, h, "docker.containers.logs", `{"id":"`+id+`","tail":5}`, &l); err != nil {
		t.Fatal(err)
	}
	var img struct{ Image string }
	_ = json.Unmarshal(in.Inspect, &img)
	if err := call(t, h, "docker.images.history", `{"id":"`+img.Image+`"}`, nil); err != nil {
		t.Errorf("history %s: %v", img.Image, err)
	}
	t.Logf("%d containers; %s risks %v, %d log lines", len(cs), cs[0].Names, in.Risks, len(l.Lines))
}

func TestDockerEvents(t *testing.T) {
	events := strings.Join([]string{
		`{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{"name":"web","image":"nginx","SECRET_LABEL":"x"}},"timeNano":5}`,
		`{"Type":"container","Action":"exec_start: pg_isready","Actor":{"ID":"c1"}}`, // health check noise
		`{"Type":"daemon","Action":"reload","Actor":{"ID":"d"}}`,
		`not json`,
		`{"Type":"volume","Action":"create","Actor":{"ID":"v1","Attributes":{"name":"` + strings.Repeat("v", 70<<10) + `"}}}`, // oversized
		`{"Type":"image","Action":"pull","Actor":{"ID":"nginx:latest","Attributes":{"name":"nginx"}},"timeNano":7}`,
	}, "\n") + "\n"
	h, f, _ := dockerHarness(t, false, map[string]answer{"GET /events": {body: events}})
	var got []DockerEvent
	err := h.client().Stream(context.Background(), "docker.events", nil, func(raw json.RawMessage) error {
		if strings.Contains(string(raw), "SECRET_LABEL") {
			t.Errorf("attributes forwarded: %s", raw)
		}
		var e DockerEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		got = append(got, e)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "event stream ended") {
		t.Fatalf("end: %v", err)
	}
	want := []DockerEvent{
		{Type: "container", Action: "start", ID: "c1", Name: "web", Image: "nginx", Time: 5},
		{Type: "image", Action: "pull", ID: "nginx:latest", Name: "nginx", Time: 7},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
	reqs := f.requests()
	if len(reqs) != 1 || reqs[0] != "GET /events?"+(url.Values{"filters": {`{"type":["container","image","network","volume"]}`}}).Encode() {
		t.Fatalf("requests %v", reqs)
	}
	if err := h.client().Stream(context.Background(), "docker.events", json.RawMessage(`{"since":0}`), func(json.RawMessage) error { return nil }); code(err) != CodeBadParams {
		t.Fatalf("params: %v", err)
	}
}

func TestDockerLogsFollow(t *testing.T) {
	frame := func(stream byte, s string) string {
		h := make([]byte, 8)
		h[0] = stream
		binary.BigEndian.PutUint32(h[4:], uint32(len(s))) // #nosec G115 -- short test strings
		return string(h) + s
	}
	long := strings.Repeat("x", maxFollowLine+100)
	h, f, _ := dockerHarness(t, false, map[string]answer{
		"GET /containers/web/json": {body: `{"Config":{"Tty":false}}`},
		// A line split over two frames, an over-long line, a last line
		// without a newline.
		"GET /containers/web/logs": {body: frame(1, "hel") + frame(1, "lo\nworld\n") + frame(2, "oops\r\n") + frame(1, long+"\n") + frame(1, "end")},
		"GET /containers/tty/json": {body: `{"Config":{"Tty":true}}`},
		"GET /containers/tty/logs": {body: "a\r\nb\xff\n"},
	})
	follow := func(params string) ([]LogLine, error) {
		var got []LogLine
		err := h.client().Stream(context.Background(), "docker.containers.logs.follow", json.RawMessage(params), func(raw json.RawMessage) error {
			var batch []LogLine
			if err := json.Unmarshal(raw, &batch); err != nil {
				return err
			}
			got = append(got, batch...)
			return nil
		})
		return got, err
	}
	got, err := follow(`{"id":"web","tail":10}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []LogLine{{"stdout", "hello"}, {"stdout", "world"}, {"stderr", "oops"}, {"stdout", long[:maxFollowLine]}, {"stdout", "end"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%d lines: %.200v", len(got), got)
	}
	if r := f.requests()[1]; r != "GET /containers/web/logs?follow=true&stderr=true&stdout=true&tail=10" {
		t.Fatalf("%s", r)
	}
	if got, err := follow(`{"id":"tty"}`); err != nil || !slices.Equal(got, []LogLine{{"stdout", "a"}, {"stdout", "b�"}}) {
		t.Fatalf("tty: %v %+v", err, got)
	}
	for _, bad := range []string{`{"id":"../x"}`, `{"id":"web","tail":-1}`, `{"id":"web","tail":5001}`, `{"id":"web","since":1}`} {
		if _, err := follow(bad); code(err) != CodeBadParams {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// execEngine plays the Engine's exec API: the shell answers each input
// line with "got <line>" and exits on "exit".
type execEngine struct {
	mu      sync.Mutex
	reqs    []string
	inspect string
}

func newExecEngine(t *testing.T, inspect string) (*execEngine, string) {
	t.Helper()
	e := &execEngine{inspect: inspect}
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	eid := strings.Repeat("e", 64)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/"+DockerAPI)
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.reqs = append(e.reqs, r.Method+" "+path+"?"+r.URL.RawQuery+" "+string(body))
		e.mu.Unlock()
		switch {
		case r.Method == "GET" && path == "/containers/web/json":
			_, _ = w.Write([]byte(e.inspect))
		case r.Method == "POST" && path == "/containers/web/exec":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"` + eid + `"}`))
		case r.Method == "POST" && path == "/exec/"+eid+"/resize":
			w.WriteHeader(http.StatusOK)
		case r.Method == "POST" && path == "/exec/"+eid+"/start" && r.Header.Get("Upgrade") == "tcp":
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n$ ")
			_ = rw.Flush()
			for {
				line, err := rw.ReadString('\n')
				if err != nil || line == "exit\n" {
					return
				}
				_, _ = rw.WriteString("got " + line)
				_ = rw.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return e, sock
}

func (e *execEngine) requests() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.reqs)
}

const runningSafe = `{"State":{"Running":true},"HostConfig":{"NetworkMode":"bridge"}}`

func TestDockerExec(t *testing.T) {
	e, sock := newExecEngine(t, runningSafe)
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(NewDocker(sock))...) })
	h.auth.allow = true
	s, err := h.client().Open(context.Background(), "docker.containers.exec", map[string]any{"id": "web", "cols": 120, "rows": 40})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recv := func() ExecOut {
		t.Helper()
		raw, err := s.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		var o ExecOut
		if err := json.Unmarshal(raw, &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := recv(); !o.Ready {
		t.Fatalf("first item %+v", o)
	}
	if o := recv(); string(o.Out) != "$ " {
		t.Fatalf("prompt %q", o.Out)
	}
	_ = s.Send(ExecIn{In: []byte("ls\n")})
	if o := recv(); string(o.Out) != "got ls\n" {
		t.Fatalf("output %q", o.Out)
	}
	_ = s.Send(ExecIn{Resize: &[2]int{100, 30}})
	_ = s.Send(ExecIn{Resize: &[2]int{0, 30}})        // ignored
	_ = s.Send(json.RawMessage(`{"cmd":"rm -rf /"}`)) // ignored: unknown field
	_ = s.Send(ExecIn{In: []byte("exit\n")})
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
	reqs := e.requests()
	if len(reqs) != 4 {
		t.Fatalf("requests %q", reqs)
	}
	var create struct {
		Cmd         []string
		Tty         bool
		ConsoleSize [2]int
		User        string
	}
	_ = json.Unmarshal([]byte(reqs[1][strings.Index(reqs[1], " {")+1:]), &create)
	if !slices.Equal(create.Cmd, []string{"/bin/sh"}) || !create.Tty || create.ConsoleSize != [2]int{40, 120} || create.User != "" {
		t.Fatalf("create %s", reqs[1])
	}
	if !strings.Contains(reqs[3], "/resize?h=30&w=100") {
		t.Fatalf("resize %s", reqs[3])
	}
	if got := h.auth.asked; len(got) != 1 || !strings.HasPrefix(got[0], ActionDockerExec) {
		t.Fatalf("polkit asked %v", got)
	}
}

func TestDockerExecRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		inspect, params string
		allow           bool
		want            int
	}{
		"unauthorized":  {runningSafe, `{"id":"web"}`, false, CodeUnauthorized},
		"privileged":    {`{"State":{"Running":true},"HostConfig":{"Privileged":true}}`, `{"id":"web"}`, true, CodeRefused},
		"docker socket": {`{"State":{"Running":true},"HostConfig":{"Binds":["/run/docker.sock:/var/run/docker.sock"]}}`, `{"id":"web"}`, true, CodeRefused},
		"host pid":      {`{"State":{"Running":true},"HostConfig":{"PidMode":"host"}}`, `{"id":"web"}`, true, CodeRefused},
		"stopped":       {`{"State":{"Running":false},"HostConfig":{}}`, `{"id":"web"}`, true, CodeConflict},
		"paused":        {`{"State":{"Running":true,"Paused":true},"HostConfig":{}}`, `{"id":"web"}`, true, CodeConflict},
		"command":       {runningSafe, `{"id":"web","cmd":["bash"]}`, true, CodeBadParams},
		"user":          {runningSafe, `{"id":"web","user":"root"}`, true, CodeBadParams},
		"bad id":        {runningSafe, `{"id":"../web"}`, true, CodeBadParams},
		"bad size":      {runningSafe, `{"id":"web","cols":0}`, true, CodeBadParams},
	} {
		t.Run(name, func(t *testing.T) {
			e, sock := newExecEngine(t, tc.inspect)
			h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(NewDocker(sock))...) })
			h.auth.allow = tc.allow
			s, err := h.client().Open(context.Background(), "docker.containers.exec", json.RawMessage(tc.params))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if _, err := s.Recv(); code(err) != tc.want {
				t.Fatalf("got %v, want code %d", err, tc.want)
			}
			for _, r := range e.requests() {
				if strings.Contains(r, "/exec") {
					t.Fatalf("reached exec: %s", r)
				}
			}
		})
	}
}

func TestDockerExecLimit(t *testing.T) {
	_, sock := newExecEngine(t, runningSafe)
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, DockerMethods(NewDocker(sock))...) })
	h.auth.allow = true
	var open []*Session
	for range maxExecs {
		s, _ := h.client().Open(context.Background(), "docker.containers.exec", map[string]string{"id": "web"})
		if _, err := s.Recv(); err != nil {
			t.Fatal(err)
		}
		open = append(open, s)
	}
	s, _ := h.client().Open(context.Background(), "docker.containers.exec", map[string]string{"id": "web"})
	if _, err := s.Recv(); code(err) != CodeRefused {
		t.Fatalf("third session: %v", err)
	}
	for _, o := range append(open, s) {
		_ = o.Close()
	}
}

func TestDockerImagesListSharedSize(t *testing.T) {
	h, f, _ := dockerHarness(t, false, map[string]answer{"GET /images/json": {body: `[]`}})
	if err := call(t, h, "docker.images.list", `{"shared_size":true}`, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, h, "docker.images.list", `{"all":true}`, nil); code(err) != CodeBadParams {
		t.Fatalf("unknown param: %v", err)
	}
	if r := f.requests(); len(r) != 1 || r[0] != "GET /images/json?shared-size=true" {
		t.Fatalf("%v", r)
	}
}
