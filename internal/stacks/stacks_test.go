package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const testSpec = `{"version":1,"name":"web","services":{"app":{"image":"nginx:1.27","ports":[{"host":8080,"container":80}]},
"db":{"image":"postgres:17","pull_policy":"never"}},"volumes":{"data":{},"ext":{"external":true,"name":"shared"}}}`

type brokerCall struct {
	method string
	params string
}

type fakeBroker struct {
	calls  []brokerCall
	status []broker.StackInfo
	err    error // returned by state-changing calls
}

func (f *fakeBroker) Call(_ context.Context, method string, params, out any) error {
	b, _ := json.Marshal(params)
	f.calls = append(f.calls, brokerCall{method, string(b)})
	switch method {
	case "stack.status":
		var p struct{ Name string }
		_ = json.Unmarshal(b, &p)
		for _, s := range f.status {
			if s.Name == p.Name {
				return remarshal([]broker.StackInfo{s}, out)
			}
		}
		return broker.Errorf(broker.CodeNotFound, "no such stack")
	case "docker.images.inspect":
		return remarshal(map[string][]string{"RepoDigests": {"nginx@sha256:" + strings.Repeat("1", 64)}}, out)
	}
	return f.err
}

func remarshal(v, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

type fakeTags struct{ status, remote string }

func (f fakeTags) Check(_ context.Context, tags, _ []string) []docker.TagUpdate {
	return []docker.TagUpdate{{Tag: tags[0], Status: f.status, Remote: f.remote}}
}

func rig(t *testing.T, tags fakeTags) (*actions.Registry, *fakeBroker) {
	t.Helper()
	fb := &fakeBroker{status: []broker.StackInfo{{Name: "web", SpecSHA256: strings.Repeat("a", 64), Spec: json.RawMessage(testSpec), State: "running",
		Containers: []broker.StackContainer{{ID: "c1", Name: "web-app-1", Service: "app", State: "running", ImageID: "sha256:" + strings.Repeat("2", 64)}}}}}
	reg := actions.NewRegistry(nil)
	Register(reg, fb, tags)
	return reg, fb
}

func plan(t *testing.T, reg *actions.Registry, name string, r actions.Request) actions.Plan {
	t.Helper()
	a, ok := reg.Lookup(Kind + "/" + name)
	if !ok {
		t.Fatalf("no action %s", name)
	}
	if err := a.Validate(r); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

func run(t *testing.T, reg *actions.Registry, name string, r actions.Request, p actions.Plan) actions.Result {
	t.Helper()
	a, _ := reg.Lookup(Kind + "/" + name)
	res, err := a.Run(context.Background(), r, p)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestDeploy(t *testing.T) {
	reg, fb := rig(t, fakeTags{})
	r := actions.Request{Target: "web", Params: map[string]string{"spec": testSpec}}
	p := plan(t, reg, "deploy", r)
	if len(p.Blocked) != 0 || len(p.Items) != 2 || !strings.Contains(p.Items[0].Detail+p.Items[1].Detail, "port 127.0.0.1:8080→80") {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(p.Items[0].Detail, "restart unless-stopped") {
		t.Fatalf("restart policy not shown: %+v", p.Items)
	}
	if res := run(t, reg, "deploy", r, p); res.Done != 2 || len(res.Failed) != 0 {
		t.Fatalf("%+v", res)
	}
	last := fb.calls[len(fb.calls)-1]
	if last.method != "stack.deploy" || !strings.Contains(last.params, `"ack_flags":[]`) {
		t.Fatalf("%+v", last)
	}

	// Refusals show in the preview and nothing reaches the broker.
	for name, s := range map[string]string{
		"other name": strings.Replace(testSpec, `"name":"web"`, `"name":"blog"`, 1),
		"privileged": strings.Replace(testSpec, `"image":"nginx:1.27"`, `"image":"nginx:1.27","privileged":true`, 1),
		"yaml":       "services:\n  a: {}",
	} {
		before := len(fb.calls)
		r := actions.Request{Target: "web", Params: map[string]string{"spec": s}}
		p := plan(t, reg, "deploy", r)
		if len(p.Blocked) == 0 || p.State != nil {
			t.Errorf("%s: %+v", name, p)
		}
		res := run(t, reg, "deploy", r, p)
		if res.Done != 0 || len(fb.calls) != before {
			t.Errorf("%s: ran %+v", name, res)
		}
	}
	a, _ := reg.Lookup("stack/deploy")
	for name, r := range map[string]actions.Request{
		"no spec":     {Target: "web"},
		"bad target":  {Target: "../x", Params: map[string]string{"spec": testSpec}},
		"extra param": {Target: "web", Params: map[string]string{"spec": testSpec, "flags": "x"}},
		"huge":        {Target: "web", Params: map[string]string{"spec": strings.Repeat("a", 49<<10)}},
	} {
		if a.Validate(r) == nil {
			t.Errorf("%s validated", name)
		}
	}
	if a.Risk != actions.Destructive {
		t.Fatal("deploy must be previewed")
	}
}

func TestUpdate(t *testing.T) {
	remote := "sha256:" + strings.Repeat("b", 64)
	reg, fb := rig(t, fakeTags{status: docker.UpdateNewer, remote: remote})
	r := actions.Request{Target: "web"}
	p := plan(t, reg, "update", r)
	if len(p.Items) != 2 || !strings.Contains(p.Items[1].Detail, "local image") || !strings.Contains(p.Items[0].Detail, "new digest bbbbbbbbbbbb") {
		t.Fatalf("%+v", p.Items)
	}
	run(t, reg, "update", r, p)
	last := fb.calls[len(fb.calls)-1]
	if last.method != "stack.update" || !strings.Contains(last.params, `"pins":{"app":"`+remote+`"}`) || !strings.Contains(last.params, strings.Repeat("a", 64)) {
		t.Fatalf("%+v", last)
	}

	reg, fb = rig(t, fakeTags{status: docker.UpdateCurrent})
	p = plan(t, reg, "update", r)
	if p.State != nil || !strings.Contains(p.Summary, "up to date") {
		t.Fatalf("%+v", p)
	}
	before := len(fb.calls)
	if res := run(t, reg, "update", r, p); res.Done != 0 || len(fb.calls) != before {
		t.Fatal("an update with nothing new reached the broker")
	}
}

func TestRemove(t *testing.T) {
	reg, fb := rig(t, fakeTags{})
	p := plan(t, reg, "remove", actions.Request{Target: "web"})
	if p.Confirm != "" || !strings.Contains(p.Summary, "volumes and bind folders stay") {
		t.Fatalf("%+v", p)
	}
	r := actions.Request{Target: "web", Params: map[string]string{"volumes": "true"}}
	p = plan(t, reg, "remove", r)
	if p.Confirm != "web" {
		t.Fatalf("no typed confirmation: %+v", p)
	}
	var vols []string
	for _, it := range p.Items {
		if strings.HasPrefix(it.ID, "volume:") {
			vols = append(vols, it.Name)
		}
	}
	if len(vols) != 1 || vols[0] != "web_data" {
		t.Fatalf("volumes %v (an external volume is not the stack's)", vols)
	}
	run(t, reg, "remove", r, p)
	last := fb.calls[len(fb.calls)-1]
	if last.method != "stack.remove" || !strings.Contains(last.params, `"remove_volumes":true`) || !strings.Contains(last.params, `"typed_name":"web"`) {
		t.Fatalf("%+v", last)
	}
	a, _ := reg.Lookup("stack/remove")
	if a.Validate(actions.Request{Target: "web", Params: map[string]string{"volumes": "yes"}}) == nil {
		t.Fatal("volumes=yes validated")
	}
	if p := plan(t, reg, "remove", actions.Request{Target: "nope"}); len(p.Blocked) != 1 || p.Blocked[0].Reason != ReasonNotFound {
		t.Fatalf("%+v", p)
	}
}

func TestControl(t *testing.T) {
	reg, fb := rig(t, fakeTags{})
	for verb, risk := range map[string]actions.Risk{"start": actions.Safe, "stop": actions.Reversible, "restart": actions.Reversible} {
		a, _ := reg.Lookup("stack/" + verb)
		if a.Risk != risk {
			t.Errorf("%s risk %s", verb, a.Risk)
		}
		r := actions.Request{Target: "web"}
		run(t, reg, verb, r, plan(t, reg, verb, r))
		last := fb.calls[len(fb.calls)-1]
		if last.method != "stack.control" || last.params != `{"name":"web","verb":"`+verb+`"}` {
			t.Errorf("%+v", last)
		}
	}
	fb.err = broker.ErrUnauthorized
	r := actions.Request{Target: "web"}
	if res := run(t, reg, "stop", r, plan(t, reg, "stop", r)); len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnauthorized {
		t.Fatalf("%+v", res)
	}
	fb.err = broker.Errorf(broker.CodeRefused, "the app was stopped: its mounts are not the checked ones")
	if res := run(t, reg, "start", r, plan(t, reg, "start", r)); len(res.Failed) != 1 || res.Failed[0].Reason != ReasonRefused || !strings.Contains(res.Failed[0].Detail, "mounts") {
		t.Fatalf("%+v", res)
	}
	fb.err = broker.ErrUnavailable
	a, _ := reg.Lookup("stack/stop")
	if _, err := a.Run(context.Background(), r, plan(t, reg, "stop", r)); err == nil {
		t.Fatal("an unavailable broker is not an error")
	}
	if a.Validate(actions.Request{Target: "web", Params: map[string]string{"x": "1"}}) == nil {
		t.Fatal("params accepted")
	}
}

func TestRoutes(t *testing.T) {
	_, fb := rig(t, fakeTags{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/stacks", HandleList(&listBroker{fb}))
	mux.HandleFunc("GET /api/v1/stacks/{name}", HandleOne(fb))
	for path, want := range map[string]int{"/api/v1/stacks": 200, "/api/v1/stacks/web": 200, "/api/v1/stacks/nope": 404, "/api/v1/stacks/Bad%20Name": 400} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	rec := httptest.NewRecorder()
	HandleList(errBroker{broker.ErrNotMember})(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
}

// listBroker answers a status call without a name with every stack.
type listBroker struct{ *fakeBroker }

func (l *listBroker) Call(ctx context.Context, method string, params, out any) error {
	if method == "stack.status" && params == nil {
		return remarshal(l.status, out)
	}
	return l.fakeBroker.Call(ctx, method, params, out)
}

type errBroker struct{ err error }

func (e errBroker) Call(context.Context, string, any, any) error { return e.err }

type memAudit struct{ entries []store.AuditEntry }

func (m *memAudit) Audit(_ context.Context, e store.AuditEntry) error {
	m.entries = append(m.entries, e)
	return nil
}

type secretBroker struct {
	calls []string
	err   error
}

func (s *secretBroker) Call(_ context.Context, method string, params, out any) error {
	b, _ := json.Marshal(params)
	s.calls = append(s.calls, method+" "+string(b))
	if s.err != nil {
		return s.err
	}
	if method == "stack.secrets.reveal" {
		return remarshal(map[string]string{"value": "hunter2$x"}, out)
	}
	return nil
}

func TestSecretRoutes(t *testing.T) {
	sb, audit := &secretBroker{}, &memAudit{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stacks/{name}/secrets", HandleSecretsSet(sb, audit))
	mux.HandleFunc("POST /api/v1/stacks/{name}/secrets/{key}/reveal", HandleReveal(sb, audit))
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}
	if rec := do("/api/v1/stacks/web/secrets", `{"set":{"PW":"hunter2$x"}}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !strings.Contains(sb.calls[0], `"set":{"PW":"hunter2$x"}`) {
		t.Fatalf("%v", sb.calls)
	}
	rec := do("/api/v1/stacks/web/secrets/PW/reveal", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hunter2$x") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Code, rec.Body.String(), rec.Header())
	}
	for _, e := range audit.entries {
		b, _ := json.Marshal(e.Params)
		if strings.Contains(string(b), "hunter2") {
			t.Fatalf("the audit holds a value: %s", b)
		}
	}
	if len(audit.entries) != 2 || audit.entries[0].Action != "stack.secrets.set" || audit.entries[1].Target != "web" {
		t.Fatalf("%+v", audit.entries)
	}
	for path, body := range map[string]string{ // #nosec G101 -- request paths and bodies of a test
		"/api/v1/stacks/web/secrets":                  `{"set":{"PW":"x"},"extra":1}`,
		"/api/v1/stacks/Bad/secrets":                  `{"set":{"PW":"x"}}`,
		"/api/v1/stacks/web/secrets/A-B/reveal":       "",
		"/api/v1/stacks/web/secrets/COMPOSE_X/reveal": "",
	} {
		if rec := do(path, body); rec.Code != 400 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	sb.err = broker.ErrUnauthorized
	if rec := do("/api/v1/stacks/web/secrets/PW/reveal", ""); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if last := audit.entries[len(audit.entries)-1]; last.Result != "refused: not authorized" {
		t.Fatalf("%+v", last)
	}
	sb.err = broker.Errorf(broker.CodeBadParams, "no service of web names the secret OTHER")
	if rec := do("/api/v1/stacks/web/secrets", `{"set":{"OTHER":"hunter2"}}`); rec.Code != 422 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestDeployDanger(t *testing.T) {
	reg, fb := rig(t, fakeTags{})
	spec := strings.Replace(strings.Replace(testSpec, `"host":8080`, `"host_ip":"0.0.0.0","host":8080`, 1),
		`"name":"web",`, `"name":"web","opt_in":["port:0.0.0.0:8080/tcp"],`, 1)
	r := actions.Request{Target: "web", Params: map[string]string{"spec": spec}}
	p := plan(t, reg, "deploy", r)
	if len(p.Blocked) != 0 || p.Confirm != "web" || !strings.Contains(p.Summary, "1 danger flags") {
		t.Fatalf("%+v", p)
	}
	var flagged bool
	for _, it := range p.Items {
		if it.ID == "flag:port:0.0.0.0:8080/tcp" && strings.Contains(it.Detail, "reachable from the network") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("no danger item: %+v", p.Items)
	}
	run(t, reg, "deploy", r, p)
	last := fb.calls[len(fb.calls)-1]
	if last.method != "stack.deploy" || !strings.Contains(last.params, `"ack_flags":["port:0.0.0.0:8080/tcp"]`) {
		t.Fatalf("%+v", last)
	}
	for f, want := range map[string]string{"cap:NET_ADMIN": "capability NET_ADMIN", "docker-socket": "root on the host", "network:host": "network", "x": "x"} {
		if !strings.Contains(FlagMeaning(f), want) {
			t.Errorf("%s: %s", f, FlagMeaning(f))
		}
	}
}

func TestDangerApps(t *testing.T) {
	flagged := strings.Replace(strings.Replace(testSpec, `"host":8080`, `"host_ip":"0.0.0.0","host":8080`, 1),
		`"name":"web",`, `"name":"web","opt_in":["port:0.0.0.0:8080/tcp"],`, 1)
	fb := &fakeBroker{status: []broker.StackInfo{
		{Name: "web", Spec: json.RawMessage(flagged)},
		{Name: "plain", Spec: json.RawMessage(strings.Replace(testSpec, `"name":"web"`, `"name":"plain"`, 1))},
	}}
	got, err := DangerApps(context.Background(), &listBroker{fb})
	if err != nil || len(got) != 1 || got[0].Name != "web" || strings.Join(got[0].Flags, ",") != "port:0.0.0.0:8080/tcp" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := DangerApps(context.Background(), errBroker{broker.ErrNotMember}); err == nil {
		t.Fatal("an error was swallowed")
	}
}
