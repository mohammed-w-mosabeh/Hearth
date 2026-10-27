package docker

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

func TestStartOrder(t *testing.T) {
	ms := []member{
		{Name: "app-1", Service: "app", Deps: []string{"db", "cache"}},
		{Name: "worker-1", Service: "worker", Deps: []string{"app"}},
		{Name: "cache-1", Service: "cache"},
		{Name: "db-1", Service: "db"},
		{Name: "a-1", Service: "a", Deps: []string{"b"}}, // a cycle
		{Name: "b-1", Service: "b", Deps: []string{"a"}},
		{Name: "x-1", Service: "x", Deps: []string{"missing"}},
	}
	var names []string
	for _, m := range startOrder(ms) {
		names = append(names, m.Name)
	}
	pos := func(n string) int { return slices.Index(names, n) }
	if len(names) != 7 || pos("db-1") > pos("app-1") || pos("cache-1") > pos("app-1") || pos("app-1") > pos("worker-1") {
		t.Fatalf("order %v", names)
	}
	if got := dependsOn("pgsql:service_started:false, redis:service_healthy:true,"); !slices.Equal(got, []string{"pgsql", "redis"}) {
		t.Fatalf("deps %v", got)
	}
}

const projectList = `[
 {"Id":"w","Names":["/shop-web-1"],"State":"running","Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web","com.docker.compose.depends_on":"db:service_started:false"}},
 {"Id":"d","Names":["/shop-db-1"],"State":"running","Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db"}},
 {"Id":"o","Names":["/other"],"State":"running","Labels":{"com.docker.compose.project":"shopping"}}
]`

func projectReg(f *fakeBroker) func(action, target string) (int, string) {
	reg := actions.NewRegistry(nopAudit{})
	RegisterProjects(reg, f)
	return func(action, target string) (int, string) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"target":"`+target+`","params":{}}`))
		req.SetPathValue("kind", "project")
		req.SetPathValue("action", action)
		rec := httptest.NewRecorder()
		reg.Handle(rec, req)
		return rec.Code, rec.Body.String()
	}
}

func actedOn(f *fakeBroker) []string {
	var out []string
	for _, c := range f.called() {
		if !strings.HasPrefix(c, "docker.containers.list") {
			out = append(out, c)
		}
	}
	return out
}

func TestProjectActions(t *testing.T) {
	f := &fakeBroker{answers: map[string]string{"docker.containers.list": projectList}, errs: map[string]error{}}
	post := projectReg(f)

	if code, body := post("stop", "shop"); code != 200 || !strings.Contains(body, `"done":2`) {
		t.Fatalf("stop %d %s", code, body)
	}
	// Dependents stop first; the other project is untouched.
	if got := actedOn(f); !slices.Equal(got, []string{`docker.containers.stop {"id":"w"}`, `docker.containers.stop {"id":"d"}`}) {
		t.Fatalf("stop calls %v", got)
	}
	f.calls = nil
	_, _ = post("restart", "shop")
	if got := actedOn(f); !slices.Equal(got, []string{
		`docker.containers.stop {"id":"w"}`, `docker.containers.stop {"id":"d"}`,
		`docker.containers.start {"id":"d"}`, `docker.containers.start {"id":"w"}`,
	}) {
		t.Fatalf("restart calls %v", got)
	}

	// One container failing does not stop the others.
	f.calls = nil
	f.errs["docker.containers.start"] = broker.Errorf(broker.CodeFailed, "docker: port is already allocated")
	_, body := post("start", "shop")
	var res actions.Result
	_ = json.Unmarshal([]byte(body), &res)
	if res.Done != 0 || len(res.Failed) != 2 || res.Failed[0].Detail != "port is already allocated" {
		t.Fatalf("start %s", body)
	}
	// A refused password is asked once, not per container.
	f.calls = nil
	f.errs["docker.containers.start"] = broker.ErrUnauthorized
	_, body = post("start", "shop")
	if len(actedOn(f)) != 1 || !strings.Contains(body, `"reason":"unauthorized"`) {
		t.Fatalf("unauthorized %v %s", actedOn(f), body)
	}

	if _, body := post("start", "nothing"); !strings.Contains(body, `"reason":"not-found"`) {
		t.Fatalf("missing project %s", body)
	}
	for _, bad := range []string{"Shop", "-x", "a/b", ""} {
		if code, _ := post("start", bad); code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	f.errs["docker.containers.list"] = broker.ErrUnavailable
	if code, _ := post("start", "shop"); code != 503 {
		t.Fatalf("no broker %d", code)
	}
}
