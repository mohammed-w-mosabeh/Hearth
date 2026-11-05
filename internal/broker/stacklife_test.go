package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

const lifeSpec = `{"version":1,"name":"web","bind_roots":["/srv/site"],"services":{"app":{"image":"nginx:1.27",
"environment":{"TZ":"UTC","DB_PASSWORD":"hunter2","DSN":"pg://u:p@db/x"},
"volumes":[{"type":"bind","source":"/srv/site","target":"/usr/share/nginx/html"}]}}}`

type fakeStackEngine struct {
	containers []StackContainer
	pulls      []string
	err        error
}

func (f *fakeStackEngine) StackContainers(context.Context, string) ([]StackContainer, error) {
	return slices.Clone(f.containers), f.err
}

func (f *fakeStackEngine) PullTag(_ context.Context, name, digest, tag string) error {
	f.pulls = append(f.pulls, name+"@"+digest+"->"+tag)
	return nil
}

type fakePaths struct {
	first, second pathcheck.Reply
	checks        int
	rechecks      int
}

func (f *fakePaths) Check(context.Context, json.RawMessage) (pathcheck.Reply, error) {
	f.checks++
	return f.first, nil
}

func (f *fakePaths) Recheck(_ context.Context, _ json.RawMessage, first pathcheck.Reply) (pathcheck.Reply, error) {
	f.rechecks++
	if !pathcheck.Same(first, f.second) {
		return pathcheck.Reply{Refused: []string{"a bind source resolved differently between the two checks"}}, nil
	}
	return f.second, nil
}

func okPaths() pathcheck.Reply {
	r := pathcheck.Reply{OK: true, Refused: []string{}, Roots: map[string]string{"/srv/site": "/srv/site"},
		Binds: []pathcheck.Resolved{{Real: "/srv/site", Dev: "8:1", FSRoot: "/srv/site"}}}
	r.Binds[0].Service, r.Binds[0].Source, r.Binds[0].Target = "app", "/srv/site", "/usr/share/nginx/html"
	return r
}

type lifeRig struct {
	store   StackStore
	engine  *fakeStackEngine
	paths   *fakePaths
	ran     []string // verbs the unit ran
	proc    string
	methods map[string]Method
}

func newLifeRig(t *testing.T) *lifeRig {
	t.Helper()
	r := &lifeRig{store: StackStore{Dir: filepath.Join(t.TempDir(), "stacks")}, engine: &fakeStackEngine{},
		paths: &fakePaths{first: okPaths(), second: okPaths()}, proc: t.TempDir(), methods: map[string]Method{}}
	if err := os.MkdirAll(filepath.Join(r.proc, "42"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.mountinfo(t, "1 0 0:5 / / rw - overlay overlay rw\n2 1 8:1 /srv/site /usr/share/nginx/html rw - ext4 /dev/sda1 rw\n")
	run := func(_ context.Context, unit string) error {
		name := strings.TrimSuffix(strings.TrimPrefix(unit, StackUnitPrefix), ".service")
		args, err := r.store.Read(name, FileArgs)
		if err != nil {
			return err
		}
		for v, a := range StackVerbs {
			if string(args) == "STACK_ARGS="+a+"\n" {
				r.ran = append(r.ran, v)
				switch v {
				case "up", "restart":
					r.engine.containers = []StackContainer{{ID: "c1", Service: "app", State: "running", Stack: name, PID: 42}}
				case "stop":
					r.engine.containers = []StackContainer{{ID: "c1", Service: "app", State: "exited", Stack: name}}
				}
				return nil
			}
		}
		return errors.New("unknown args " + string(args))
	}
	for _, m := range StackLifeMethods(StackConfig{Store: r.store, Paths: r.paths, Engine: r.engine, Run: run, ProcRoot: r.proc}) {
		r.methods[m.Name] = m
	}
	return r
}

func (r *lifeRig) mountinfo(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.proc, "42", "mountinfo"), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// call runs a method as the server does: Check before polkit, then Handle.
func (r *lifeRig) call(t *testing.T, name string, params any) (any, error) {
	t.Helper()
	m, ok := r.methods[name]
	if !ok {
		t.Fatalf("no method %s", name)
	}
	p, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if m.Check != nil {
		if err := m.Check(context.Background(), p); err != nil {
			return nil, err
		}
	}
	return m.Handle(context.Background(), Caller{}, p)
}

func errCode(err error) int {
	var be *Error
	if errors.As(err, &be) {
		return be.Code
	}
	return 0
}

func deployParams(spec string, flags ...string) map[string]any {
	if flags == nil {
		flags = []string{}
	}
	return map[string]any{"spec": json.RawMessage(spec), "ack_flags": flags}
}

func TestStackDeploy(t *testing.T) {
	r := newLifeRig(t)
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"up"}) || r.paths.checks != 1 || r.paths.rechecks != 1 {
		t.Fatalf("ran %v, checks %d, rechecks %d", r.ran, r.paths.checks, r.paths.rechecks)
	}
	if _, err := r.store.Read("web", FileCompose); err != nil {
		t.Fatal(err)
	}
	m := r.methods["stack.deploy"]
	if !m.Privileged || m.Action != ActionStackDeploy {
		t.Fatal("deploy is not behind its polkit action")
	}
}

func TestStackDeployRefusedBeforePolkit(t *testing.T) {
	lan := strings.Replace(lifeSpec, `"volumes"`, `"ports":[{"host_ip":"0.0.0.0","host":8080,"container":80}],"volumes"`, 1)
	lan = strings.Replace(lan, `"name":"web",`, `"name":"web","opt_in":["port:0.0.0.0:8080/tcp"],`, 1)
	cases := map[string]struct {
		params any
		setup  func(r *lifeRig)
		want   string
	}{
		"not a spec":    {deployParams(`"services: {}"`), nil, ""},
		"unknown key":   {deployParams(strings.Replace(lifeSpec, `"image"`, `"privileged":true,"image"`, 1)), nil, "unknown key"},
		"unacked flag":  {deployParams(lan), nil, "port:0.0.0.0:8080/tcp"},
		"acked flag":    {deployParams(lan, "port:0.0.0.0:8080/tcp"), nil, "cannot be deployed yet"},
		"extra ack":     {deployParams(lifeSpec, "cap:NET_ADMIN"), nil, "not the spec's"},
		"unknown param": {map[string]any{"spec": json.RawMessage(lifeSpec), "ack_flags": []string{}, "x": 1}, nil, ""},
		"no spec":       {map[string]any{"ack_flags": []string{}}, nil, "no spec"},
		"foreign":       {deployParams(lifeSpec), func(r *lifeRig) { r.engine.containers = []StackContainer{{ID: "x", Service: "app"}} }, "outside Hearth"},
		"path refused": {deployParams(lifeSpec), func(r *lifeRig) {
			r.paths.first = pathcheck.Reply{Refused: []string{"app: bind source /srv/site: it resolves to /etc"}}
		}, "/etc"},
		"oversized spec": {deployParams(`{"version":1,"name":"web","services":{"a":{"image":"x","command":["` + strings.Repeat("a", 70<<10) + `"]}}}`), nil, ""},
	}
	for name, c := range cases {
		r := newLifeRig(t)
		if c.setup != nil {
			c.setup(r)
		}
		m := r.methods["stack.deploy"]
		p, _ := json.Marshal(c.params)
		err := m.Check(context.Background(), p)
		if errCode(err) != CodeBadParams || (c.want != "" && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v", name, err)
		}
		if len(r.ran) != 0 || len(r.engine.pulls) != 0 {
			t.Errorf("%s: ran %v", name, r.ran)
		}
		if _, err := os.Stat(r.store.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: wrote the store", name)
		}
	}
}

func TestStackDeployPathsMoved(t *testing.T) {
	r := newLifeRig(t)
	r.paths.second.Binds = []pathcheck.Resolved{r.paths.first.Binds[0]}
	r.paths.second.Binds[0].FSRoot = "/etc"
	_, err := r.call(t, "stack.deploy", deployParams(lifeSpec))
	if errCode(err) != CodeRefused || !strings.Contains(err.Error(), "differently") {
		t.Fatal(err)
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v", r.ran)
	}
	if _, err := r.store.Read("web", FileSpec); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("files written after a refused recheck")
	}
}

func TestStackDeployLiveMountsDiffer(t *testing.T) {
	r := newLifeRig(t)
	r.mountinfo(t, "1 0 0:5 / / rw - overlay overlay rw\n2 1 8:1 /etc /usr/share/nginx/html rw - ext4 /dev/sda1 rw\n")
	_, err := r.call(t, "stack.deploy", deployParams(lifeSpec))
	if errCode(err) != CodeRefused || !strings.Contains(err.Error(), "stopped") {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"up", "stop"}) {
		t.Fatalf("ran %v", r.ran)
	}
}

func TestStackUpdate(t *testing.T) {
	r := newLifeRig(t)
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	raw, _ := r.store.Read("web", FileSpec)
	sum := sha256.Sum256(raw)
	d := "sha256:" + strings.Repeat("ab", 32)
	good := map[string]any{"name": "web", "spec_sha256": hex.EncodeToString(sum[:]), "pins": map[string]string{"app": d}}
	for name, p := range map[string]map[string]any{
		"stale":    {"name": "web", "spec_sha256": strings.Repeat("0", 64), "pins": map[string]string{"app": d}},
		"no pins":  {"name": "web", "spec_sha256": good["spec_sha256"], "pins": map[string]string{}},
		"unknown":  {"name": "web", "spec_sha256": good["spec_sha256"], "pins": map[string]string{"db": d}},
		"digest":   {"name": "web", "spec_sha256": good["spec_sha256"], "pins": map[string]string{"app": "latest"}},
		"no stack": {"name": "nope", "spec_sha256": good["spec_sha256"], "pins": map[string]string{"app": d}},
		"bad name": {"name": "../x", "spec_sha256": good["spec_sha256"], "pins": map[string]string{"app": d}},
	} {
		if _, err := r.call(t, "stack.update", p); err == nil || (errCode(err) != CodeBadParams && errCode(err) != CodeNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(r.engine.pulls) != 0 {
		t.Fatalf("pulled %v", r.engine.pulls)
	}
	r.ran = nil
	if _, err := r.call(t, "stack.update", good); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.engine.pulls, []string{"nginx@" + d + "->1.27"}) || !slices.Equal(r.ran, []string{"up"}) {
		t.Fatalf("pulls %v, ran %v", r.engine.pulls, r.ran)
	}
}

func TestStackControl(t *testing.T) {
	r := newLifeRig(t)
	if _, err := r.call(t, "stack.control", map[string]string{"name": "web", "verb": "start"}); errCode(err) != CodeBadParams {
		t.Fatalf("missing stack: %v", err)
	}
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	r.ran, r.paths.checks = nil, 0
	for _, v := range []string{"stop", "start", "restart"} {
		if _, err := r.call(t, "stack.control", map[string]string{"name": "web", "verb": v}); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	if !slices.Equal(r.ran, []string{"stop", "up", "restart"}) || r.paths.checks != 2 {
		t.Fatalf("ran %v, path checks %d", r.ran, r.paths.checks)
	}
	for _, v := range []string{"down", "down-volumes", "up", "update", "rm", ""} {
		if _, err := r.call(t, "stack.control", map[string]string{"name": "web", "verb": v}); errCode(err) != CodeBadParams {
			t.Errorf("verb %q: %v", v, err)
		}
	}
	if m := r.methods["stack.control"]; m.Action != ActionStackControl {
		t.Fatal("control action")
	}
}

func TestStackRemove(t *testing.T) {
	r := newLifeRig(t)
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	r.ran = nil
	for name, p := range map[string]map[string]any{
		"untyped":           {"name": "web", "remove_volumes": true},
		"mistyped":          {"name": "web", "remove_volumes": true, "typed_name": "Web"},
		"typed for nothing": {"name": "web", "typed_name": "web"},
	} {
		if _, err := r.call(t, "stack.remove", p); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v", r.ran)
	}
	if _, err := r.call(t, "stack.remove", map[string]any{"name": "web", "remove_volumes": true, "typed_name": "web"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"down-volumes"}) {
		t.Fatalf("ran %v", r.ran)
	}
	if _, err := os.Stat(filepath.Join(r.store.Dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("folder left")
	}
	if m := r.methods["stack.remove"]; m.Action != ActionStackRemove {
		t.Fatal("remove action")
	}

	// Without volumes: down, which keeps them.
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	r.ran = nil
	if _, err := r.call(t, "stack.remove", map[string]any{"name": "web"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"down"}) {
		t.Fatalf("ran %v", r.ran)
	}
}

func TestStackStatus(t *testing.T) {
	r := newLifeRig(t)
	got, err := r.call(t, "stack.status", nil)
	if err != nil || len(got.([]StackInfo)) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	if _, err := r.call(t, "stack.deploy", deployParams(lifeSpec)); err != nil {
		t.Fatal(err)
	}
	r.engine.containers = append(r.engine.containers, StackContainer{ID: "f", Service: "other"}) // not Hearth's
	got, err = r.call(t, "stack.status", map[string]string{"name": "web"})
	if err != nil {
		t.Fatal(err)
	}
	list := got.([]StackInfo)
	if len(list) != 1 || list[0].State != "running" || len(list[0].Containers) != 1 || len(list[0].SpecSHA256) != 64 {
		t.Fatalf("%+v", list)
	}
	s := string(list[0].Spec)
	if strings.Contains(s, "hunter2") || strings.Contains(s, "u:p@") || !strings.Contains(s, `"TZ":"UTC"`) {
		t.Fatalf("spec %s", s)
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), `"PID"`) || strings.Contains(string(b), "pid") {
		t.Fatalf("status shows a pid: %s", b)
	}
	if _, err := r.call(t, "stack.status", map[string]string{"name": "nope"}); errCode(err) != CodeNotFound {
		t.Fatal(err)
	}
	if r.methods["stack.status"].Privileged {
		t.Fatal("status needs no polkit")
	}
}

func TestStackState(t *testing.T) {
	s, _, err := spec.Parse([]byte(`{"version":1,"name":"s","services":{"a":{"image":"x"},"b":{"image":"y"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for want, list := range map[string][]StackContainer{
		"absent":  nil,
		"stopped": {{Service: "a", State: "exited"}},
		"partial": {{Service: "a", State: "running"}, {Service: "b", State: "exited"}},
		"running": {{Service: "a", State: "running"}, {Service: "b", State: "running"}},
	} {
		if got := stackState(s, list); got != want {
			t.Errorf("%s: got %s", want, got)
		}
	}
}

func TestPathCacheExpires(t *testing.T) {
	now := time.Unix(0, 0)
	c := &pathCache{m: map[string]cachedPaths{}, now: func() time.Time { return now }}
	c.put([]byte("a"), okPaths())
	if _, ok := c.take([]byte("a")); !ok {
		t.Fatal("not kept")
	}
	if _, ok := c.take([]byte("a")); ok {
		t.Fatal("taken twice")
	}
	c.put([]byte("a"), okPaths())
	now = now.Add(pathCacheTTL + time.Second)
	if _, ok := c.take([]byte("a")); ok {
		t.Fatal("kept past its time")
	}
}

// TestStackPolicy: each stack action is in the shipped policy, with the
// retention S12b and the plan give it.
func TestStackPolicy(t *testing.T) {
	b, err := os.ReadFile("../../packaging/polkit/org.hearth.policy")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Actions []struct {
			ID     string `xml:"id,attr"`
			Active string `xml:"defaults>allow_active"`
			Any    string `xml:"defaults>allow_any"`
		} `xml:"action"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ActionStackDeploy: "auth_admin", ActionStackControl: "auth_admin_keep", ActionStackRemove: "auth_admin"}
	for _, a := range doc.Actions {
		if w, ok := want[a.ID]; ok {
			if a.Active != w || a.Any != "auth_admin" {
				t.Errorf("%s: active %s, any %s", a.ID, a.Active, a.Any)
			}
			delete(want, a.ID)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing from the policy: %v", want)
	}
	for _, m := range StackLifeMethods(StackConfig{}) {
		if m.Privileged && m.Check == nil {
			t.Errorf("%s has no check before polkit", m.Name)
		}
	}
}

// TestStackRefusedWithoutDialog: only CodeBadParams stops a call before
// polkit, so a missing stack and a bad path must come back as that, even
// when the Engine cannot be asked.
func TestStackRefusedWithoutDialog(t *testing.T) {
	r := newLifeRig(t)
	d := "sha256:" + strings.Repeat("ab", 32)
	for name, p := range map[string]any{
		"stack.control": map[string]string{"name": "web", "verb": "start"},
		"stack.remove":  map[string]string{"name": "web"},
		"stack.update":  map[string]any{"name": "web", "spec_sha256": strings.Repeat("0", 64), "pins": map[string]string{"app": d}},
	} {
		b, _ := json.Marshal(p)
		if err := r.methods[name].Check(context.Background(), b); errCode(err) != CodeBadParams || !strings.Contains(err.Error(), "no such stack") {
			t.Errorf("%s: %v", name, err)
		}
	}
	r.engine.err = ErrNoDocker
	r.paths.first = pathcheck.Reply{Refused: []string{"app: bind source /srv/site: it resolves to /etc"}}
	b, _ := json.Marshal(deployParams(lifeSpec))
	if err := r.methods["stack.deploy"].Check(context.Background(), b); errCode(err) != CodeBadParams || !strings.Contains(err.Error(), "/etc") {
		t.Fatalf("no Engine: %v", err)
	}
}
