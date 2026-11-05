package importer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

type fakeBroker struct {
	containers string
	volumes    string
	calls      []string
}

func (f *fakeBroker) Call(_ context.Context, method string, _, out any) error {
	f.calls = append(f.calls, method)
	switch method {
	case "docker.containers.list":
		return json.Unmarshal([]byte(f.containers), out)
	case "docker.volumes.list":
		return json.Unmarshal([]byte(f.volumes), out)
	}
	return errors.New("unexpected " + method)
}

const shapeContainers = `[
 {"Id":"a1","Names":["/shape-app"],"State":"running","Labels":{"com.docker.compose.project":"shape","com.docker.compose.service":"app",
  "com.docker.compose.project.working_dir":"/srv/shape","com.docker.compose.project.config_files":"/srv/shape/docker-compose.yml"}},
 {"Id":"c1","Names":["/shape-cache-1"],"State":"running","Labels":{"com.docker.compose.project":"shape","com.docker.compose.service":"cache"}},
 {"Id":"o1","Names":["/other-web"],"State":"running","Labels":{"com.docker.compose.project":"other"},"Ports":[{"IP":"0.0.0.0","PublicPort":8080,"Type":"tcp"}]}
]`

func shapeDeps(t *testing.T, fb *fakeBroker, ran *[]string) Deps {
	t.Helper()
	return Deps{Broker: fb, TZ: func() string { return "Asia/Riyadh" },
		Run: func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
			*ran = append(*ran, name+" "+strings.Join(args, " "))
			return safeexec.Result{Stdout: shape(t)}, nil
		}}
}

func TestDryRun(t *testing.T) {
	fb := &fakeBroker{containers: shapeContainers, volumes: `{"Volumes":[{"Name":"shape_model"}]}`}
	var ran []string
	res, err := DryRun(context.Background(), shapeDeps(t, fb, &ran), "shape", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "docker-compose-config compose --project-name shape --project-directory /srv/shape --file /srv/shape/docker-compose.yml config --format json"
	if len(ran) != 1 || ran[0] != want {
		t.Fatalf("ran %v", ran)
	}
	// Only reads: two lists through the broker, one rendering.
	if !slices.Equal(fb.calls, []string{"docker.containers.list", "docker.volumes.list"}) {
		t.Fatalf("calls %v", fb.calls)
	}
	if len(res.Containers) != 2 || res.Dir != "/srv/shape" {
		t.Fatalf("%+v", res)
	}
	if len(res.Volumes) != 1 || !res.Volumes[0].Exists || res.Volumes[0].Name != "shape_model" {
		t.Fatalf("volumes %+v", res.Volumes)
	}
	var conflict bool
	for _, p := range res.Ports {
		if p.Host == 8080 && p.Conflict == "other-web" {
			conflict = true
		}
		if p.Host == 9090 && p.Conflict != "" {
			t.Errorf("udp 9090 conflicts: %+v", p)
		}
	}
	if !conflict || !has(res.Refused, "port 8080/tcp is held by other-web") {
		t.Fatalf("ports %+v refused %v", res.Ports, res.Refused)
	}
	enc, _ := json.Marshal(res)
	if strings.Contains(string(enc), "s3cretvalue") || strings.Contains(string(enc), "fromfile") {
		t.Fatalf("a value is shown: %s", enc)
	}
	if !has(res.Notes, "Nothing was changed") || !has(res.Notes, "recreates the containers once") {
		t.Fatalf("notes %v", res.Notes)
	}

	// Marked plain, a name S19 masks is still masked on the page.
	res, err = DryRun(context.Background(), shapeDeps(t, fb, &ran), "shape", map[string]bool{"DB_PASSWORD": true, "TZ": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env := res.Spec.Services["app"].Environment; env["DB_PASSWORD"] != "********" || env["TZ"] != "UTC" {
		t.Fatalf("env %v", env)
	}
	if enc, _ := json.Marshal(res); strings.Contains(string(enc), "s3cretvalue") {
		t.Fatalf("shown: %s", enc)
	}
}

func TestDryRunRefusals(t *testing.T) {
	var ran []string
	managed := strings.Replace(shapeContainers, `"com.docker.compose.service":"app",`, `"com.docker.compose.service":"app","org.hearth.stack":"shape",`, 1)
	fb := &fakeBroker{containers: managed, volumes: `{"Volumes":[]}`}
	res, err := DryRun(context.Background(), shapeDeps(t, fb, &ran), "shape", nil, nil)
	if err != nil || !has(res.Refused, "already manages") || len(ran) != 0 {
		t.Fatalf("%+v %v ran %v", res, err, ran)
	}
	fb = &fakeBroker{containers: shapeContainers, volumes: `{"Volumes":[]}`}
	if _, err := DryRun(context.Background(), shapeDeps(t, fb, &ran), "nope", nil, nil); !errors.Is(err, ErrNoProject) {
		t.Fatal(err)
	}
	res, err = DryRun(context.Background(), shapeDeps(t, fb, &ran), "../x", nil, nil)
	if err != nil || !has(res.Refused, "cannot be a stack name") {
		t.Fatalf("%+v %v", res, err)
	}
	// A failed rendering does not show what Compose said.
	d := Deps{Broker: fb, Run: func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{ExitCode: 1, Stderr: []byte("invalid interpolation: hunter22")}, nil
	}}
	res, err = DryRun(context.Background(), d, "shape", nil, nil)
	if err != nil || !has(res.Refused, "could not render") {
		t.Fatalf("%+v %v", res, err)
	}
	if enc, _ := json.Marshal(res); strings.Contains(string(enc), "hunter22") {
		t.Fatalf("Compose's message is shown: %s", enc)
	}
}

func TestHandle(t *testing.T) {
	fb := &fakeBroker{containers: shapeContainers, volumes: `{"Volumes":[]}`}
	var ran []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/stacks/import/{project}", Handle(shapeDeps(t, fb, &ran)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stacks/import/shape?plain=TZ&skip=", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "s3cretvalue") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var res DryRunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Spec.Services["app"].Environment["TZ"] != "UTC" {
		t.Fatalf("%v %+v", err, res.Spec.Services["app"])
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stacks/import/nope", nil))
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}
