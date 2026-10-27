package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]ref{
		"nginx":                        {"docker.io", "library/nginx", "latest"},
		"nginx:1.27":                   {"docker.io", "library/nginx", "1.27"},
		"bitnami/redis:7":              {"docker.io", "bitnami/redis", "7"},
		"ghcr.io/photos-app/server:v3": {"ghcr.io", "photos-app/server", "v3"},
		"localhost:5000/app":           {"localhost:5000", "app", "latest"},
		"nginx@sha256:" + strings.Repeat("a", 64): {"docker.io", "library/nginx", ""},
	} {
		got, err := parseRef(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "Nginx", "a b", "nginx:", "nginx:-x", "../x"} {
		if _, err := parseRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	d := "sha256:" + strings.Repeat("b", 64)
	r, _ := parseRef("nginx:latest")
	if got := localDigest(r, []string{"other@" + d, "nginx@" + d}); got != d {
		t.Errorf("local digest %q", got)
	}
	if got := localDigest(r, []string{"docker.io/library/nginx@" + d}); got != d {
		t.Errorf("canonical local digest %q", got)
	}
}

// fakeRegistry needs a token (from /token) and serves digests by tag.
type fakeRegistry struct {
	mu      sync.Mutex
	digests map[string]string // "repo:tag" → digest
	reqs    []string
}

func (f *fakeRegistry) handler(base func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:library/nginx:pull" && r.URL.Query().Get("scope") != "repository:library/redis:pull" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+base()+`/token",service="registry.test",scope="x"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		repo, tag, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/")
		dg, ok := f.digests[repo+":"+tag]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", dg)
	})
}

func TestRegistryCheck(t *testing.T) {
	cur, old := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("0", 64)
	f := &fakeRegistry{digests: map[string]string{"library/nginx:latest": cur, "library/redis:7": cur}}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(f.handler(func() string { return srv.URL }))
	defer srv.Close()
	g := NewRegistry()
	g.Client.Transport = srv.Client().Transport
	g.Base = func(string) string { return srv.URL }

	got := g.Check(context.Background(),
		[]string{"nginx:latest", "redis:7", "redis:gone", "mine:dev", "Bad:Ref"},
		[]string{"nginx@" + cur, "redis@" + old})
	want := []string{"current", "update", "unknown", "local", "unknown"}
	for i, u := range got {
		if u.Status != want[i] {
			t.Errorf("%s: %+v, want %s", u.Tag, u, want[i])
		}
	}
	if got[2].Error != "the registry has no such tag" || got[1].Remote != cur || got[1].Local != old {
		t.Errorf("%+v", got)
	}
	for _, r := range f.reqs {
		if strings.Contains(r, "/v2/library/mine") {
			t.Errorf("asked about a local image: %s", r)
		}
	}

	// A token service that is not HTTPS is not used.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="http://evil.test/token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer plain.Close()
	g.Client.Transport = nil
	g.Base = func(string) string { return plain.URL }
	if u := g.Check(context.Background(), []string{"nginx:latest"}, []string{"nginx@" + cur}); u[0].Status != UpdateUnknown || !strings.Contains(u[0].Error, "not HTTPS") {
		t.Errorf("http realm: %+v", u)
	}
}

func TestParseTrivy(t *testing.T) {
	out := strings.Join([]string{
		"CVE-2024-1\topenssl\t3.0.1\t3.0.2\tHIGH",
		"CVE-2024-2\tzlib\t1.2\t\tCRITICAL",
		"CVE-2024-1\topenssl\t3.0.1\t3.0.2\tHIGH", // same finding in another layer
		"GHSA-xxxx-yyyy\tlodash\t4.0\t4.1\tMEDIUM",
		"<script>\tx\t1\t\tLOW",
		"short\tline",
		"CVE-2024-3\tbash\t5\t\tWEIRD",
	}, "\n")
	s := parseTrivy([]byte(out))
	if len(s.Vulns) != 4 || s.Vulns[0].ID != "CVE-2024-2" || s.Vulns[3].Severity != "UNKNOWN" {
		t.Fatalf("%+v", s)
	}
	if s.Counts["HIGH"] != 1 || s.Counts["CRITICAL"] != 1 || s.Counts["MEDIUM"] != 1 || s.Counts["UNKNOWN"] != 1 {
		t.Fatalf("counts %v", s.Counts)
	}
}

const imgID = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestImageRoutes(t *testing.T) {
	f := &fakeBroker{answers: map[string]string{
		"docker.images.inspect": `{"Id":"` + imgID + `","RepoTags":["nginx:latest"],"RepoDigests":[],"Config":{"Env":["A=1"]}}`,
		"docker.images.history": `[{"CreatedBy":"CMD [\"nginx\"]","Size":0}]`,
	}, errs: map[string]error{}}
	var ran [][]string
	x := &Images{B: f, Registry: NewRegistry(), CacheDir: "/d/hearth/trivy",
		Run: func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
			ran = append(ran, append([]string{name}, args...))
			if name == "trivy-version" {
				return safeexec.Result{Stdout: []byte("Version: 0.60.0\n")}, nil
			}
			return safeexec.Result{Stdout: []byte("CVE-1\tpkg\t1\t2\tHIGH\n")}, nil
		}}
	do := func(h http.HandlerFunc, method, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/x", nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	rec := do(x.HandleDetail, "GET", imgID)
	var d ImageDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || !strings.Contains(string(d.History), "nginx") {
		t.Fatalf("detail %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"nginx:latest", "sha256:abc", "../x"} {
		if rec := do(x.HandleDetail, "GET", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := do(x.HandleUpdates, "GET", imgID); !strings.Contains(rec.Body.String(), `"status":"local"`) {
		t.Fatalf("updates %s", rec.Body.String())
	}
	if rec := do(x.HandleTrivy, "GET", ""); !strings.Contains(rec.Body.String(), `"version":"0.60.0"`) {
		t.Fatalf("trivy %s", rec.Body.String())
	}
	rec = do(x.HandleScan, "POST", imgID)
	if !strings.Contains(rec.Body.String(), `"HIGH":1`) {
		t.Fatalf("scan %s", rec.Body.String())
	}
	last := ran[len(ran)-1]
	if last[0] != "trivy-image" || last[len(last)-1] != "nginx:latest" || last[len(last)-2] != "--cache-dir=/d/hearth/trivy" {
		t.Fatalf("ran %q", last)
	}
	// The real allowlist accepts exactly what HandleScan runs.
	if err := safeexec.Check(last[0], last[1:]...); err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	x.Run = func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{}, safeexec.ErrBinary
	}
	if rec := do(x.HandleScan, "POST", imgID); rec.Code != http.StatusNotFound {
		t.Fatalf("no trivy %d", rec.Code)
	}
	f.errs["docker.images.inspect"] = broker.ErrNoDocker
	if rec := do(x.HandleDetail, "GET", imgID); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no docker %d", rec.Code)
	}
}

func TestImageActions(t *testing.T) {
	f := &fakeBroker{answers: map[string]string{
		"docker.images.inspect":  `{"Id":"` + imgID + `","RepoTags":["nginx:latest"]}`,
		"docker.containers.list": `[{"Id":"c1","Names":["/web"],"ImageID":"` + imgID + `"},{"Id":"c2","Names":["/db"],"ImageID":"sha256:other"}]`,
	}, errs: map[string]error{}}
	reg := actions.NewRegistry(nopAudit{})
	RegisterImages(reg, f)
	post := func(action, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("kind", "image")
		req.SetPathValue("action", action)
		rec := httptest.NewRecorder()
		reg.Handle(rec, req)
		return rec
	}
	rec := post("remove", `{"target":"`+imgID+`","params":{},"preview":true}`)
	var plan struct {
		Token, Confirm string
		Blocked        []struct{ Name, Reason string }
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	if plan.Confirm != "nginx:latest" || len(plan.Blocked) != 1 || plan.Blocked[0].Name != "web" || plan.Blocked[0].Reason != "in-use" {
		t.Fatalf("preview %s", rec.Body.String())
	}
	if rec := post("remove", `{"target":"`+imgID+`","params":{},"token":"`+plan.Token+`","confirm":"nginx:latest"}`); rec.Code != 200 {
		t.Fatalf("remove %d %s", rec.Code, rec.Body.String())
	}
	if c := f.called(); c[len(c)-1] != `docker.images.remove {"force":true,"id":"`+imgID+`"}` {
		t.Fatalf("calls %v", c)
	}

	if rec := post("tag", `{"target":"`+imgID+`","params":{"repo":"me/nginx","tag":"keep"}}`); rec.Code != 200 {
		t.Fatalf("tag %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("tag", `{"target":"`+imgID+`","params":{"repo":"Me","tag":"-x"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tag %d", rec.Code)
	}
	if rec := post("pull", `{"target":"nginx:latest","params":{}}`); rec.Code != 200 {
		t.Fatalf("pull %d %s", rec.Code, rec.Body.String())
	}
	if c := f.called(); c[len(c)-1] != `docker.images.pull {"ref":"nginx:latest"}` {
		t.Fatalf("calls %v", c)
	}
	f.errs["docker.images.pull"] = broker.ErrUnauthorized
	if rec := post("pull", `{"target":"nginx:latest","params":{}}`); !strings.Contains(rec.Body.String(), `"reason":"unauthorized"`) {
		t.Fatalf("unauthorized pull %s", rec.Body.String())
	}
}

func FuzzParseRef(f *testing.F) {
	for _, s := range []string{"nginx", "ghcr.io/a/b:v", "localhost:5000/x", "../x", "a@sha256:" + strings.Repeat("a", 64)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := parseRef(s)
		if err != nil {
			return
		}
		u := "https://" + registryHost(r.Host) + "/v2/" + r.Repo + "/manifests/" + r.Tag
		if strings.ContainsAny(s, " \t\n?#%\\") || strings.Contains(r.Repo, "..") || strings.Contains(r.Host, "..") {
			t.Fatalf("accepted %q", s)
		}
		if p, err := url.Parse(u); err != nil || p.Host != registryHost(r.Host) {
			t.Fatalf("%q builds %q", s, u)
		}
	})
}
