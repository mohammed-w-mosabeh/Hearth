package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *memSettings) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memSettings) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]json.RawMessage{}
	}
	s.m[k] = v
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func portOf(t *testing.T, u string) int {
	t.Helper()
	pu, _ := url.Parse(u)
	_, p, _ := net.SplitHostPort(pu.Host)
	n, _ := strconv.Atoi(p)
	return n
}

func TestName(t *testing.T) {
	for host, want := range map[string]string{
		"cloud.localhost:8470":      "cloud",
		"CLOUD.LocalHost:8470":      "cloud",
		"my-app_2.localhost:8470":   "my-app_2",
		"cloud.localhost:8471":      "",
		"cloud.localhost":           "",
		"localhost:8470":            "",
		"127.0.0.1:8470":            "",
		"a.cloud.localhost:8470":    "",
		"cloud.localhost.evil:8470": "",
		"cloud.example.com:8470":    "",
		"..localhost:8470":          "",
		".localhost:8470":           "",
	} {
		if got := Name(host, 8470); got != want {
			t.Errorf("Name(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestConfigCheck(t *testing.T) {
	ok := Config{Port: 8470, Routes: map[string]int{"cloud": 8080}}
	if err := ok.Check(9000); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Config{
		"privileged port": {Port: 80},
		"too high":        {Port: 70000},
		"hearth's port":   {Port: 9000},
		"bad name":        {Port: 8470, Routes: map[string]int{"../x": 8080}},
		"route to hearth": {Port: 8470, Routes: map[string]int{"cloud": 9000}},
		"route to itself": {Port: 8470, Routes: map[string]int{"cloud": 8470}},
		"route port 0":    {Port: 8470, Routes: map[string]int{"cloud": 0}},
	} {
		if c.Check(9000) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAppPorts(t *testing.T) {
	s := spec.Spec{Services: map[string]spec.Service{
		"web": {Ports: []spec.Port{{Host: 8080, Container: 80}, {HostIP: "127.0.0.1", Host: 8443, Container: 443}}},
		"lan": {Ports: []spec.Port{{HostIP: "0.0.0.0", Host: 9000, Container: 80}, {HostIP: "192.168.1.5", Host: 9001, Container: 80}}},
		"dns": {Ports: []spec.Port{{Host: 5353, Container: 53, Protocol: "udp"}, {Host: 8080, Container: 81}}},
	}}
	got := AppPorts(s)
	if want := []int{8080, 8443, 9000}; len(got) != len(want) || got[0] != 8080 || got[1] != 8443 || got[2] != 9000 {
		t.Errorf("AppPorts = %v, want %v", got, want)
	}
}

type rig struct {
	p       *Proxy
	hearth  int
	backend int
	seen    chan *http.Request
	targets map[string][]int
	fail    bool
	mu      sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{seen: make(chan *http.Request, 4)}
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.seen <- req
		_, _ = io.WriteString(w, "app here")
	}))
	t.Cleanup(be.Close)
	r.backend = portOf(t, be.URL)
	hearth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "HEARTH")
	}))
	t.Cleanup(hearth.Close)
	r.hearth = portOf(t, hearth.URL)
	r.targets = map[string][]int{"cloud": {r.backend, r.hearth}, "wiki": {}}
	r.p = &Proxy{Settings: &memSettings{}, HearthPort: func() int { return r.hearth },
		Targets: func(context.Context) (map[string][]int, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.fail {
				return nil, errors.New("broker down")
			}
			return r.targets, nil
		}}
	r.p.Start(context.Background())
	t.Cleanup(r.p.Stop)
	return r
}

func get(t *testing.T, port int, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/path?q=1", nil)
	req.Host = host
	req.Header.Set("Forwarded", "for=6.6.6.6")
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestProxy(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if st := r.p.Status(ctx); st.Running || st.Enabled || st.Port != DefaultPort {
		t.Fatalf("not off by default: %+v", st)
	}
	port := freePort(t)
	if err := r.p.Apply(ctx, Config{Enabled: true, Port: port, Routes: map[string]int{"cloud": r.backend}}); err != nil {
		t.Fatal(err)
	}
	st := r.p.Status(ctx)
	if !st.Running || r.p.ln.Addr().String() != "127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("listening %+v on %v", st, r.p.ln.Addr())
	}
	host := "cloud.localhost:" + strconv.Itoa(port)
	code, body := get(t, port, host)
	if code != 200 || body != "app here" {
		t.Fatalf("proxied: %d %q", code, body)
	}
	in := <-r.seen
	if in.Host != host || in.URL.Path != "/path" || in.URL.RawQuery != "q=1" {
		t.Errorf("backend saw host %q url %v", in.Host, in.URL)
	}
	if in.Header.Get("Forwarded") != "" || in.Header.Get("X-Forwarded-For") != "127.0.0.1" || in.Header.Get("X-Forwarded-Host") != host {
		t.Errorf("forwarding headers %v", in.Header)
	}

	// Hearth is never reached: not by its own host, not by a name.
	for _, h := range []string{"localhost:" + strconv.Itoa(port), "127.0.0.1:" + strconv.Itoa(port), "nope.localhost:" + strconv.Itoa(port), "wiki.localhost:" + strconv.Itoa(port)} {
		if code, body := get(t, port, h); code == 200 || strings.Contains(body, "HEARTH") || strings.Contains(body, "app here") {
			t.Errorf("%s: %d %q", h, code, body)
		}
	}
	if err := r.p.Apply(ctx, Config{Enabled: true, Port: port, Routes: map[string]int{"cloud": r.hearth}}); err == nil {
		t.Error("a route to Hearth's port was accepted")
	}
	// Even if Hearth's port were in the saved routes, it is not a target.
	r.p.mu.Lock()
	r.p.cfg.Routes["cloud"] = r.hearth
	r.p.mu.Unlock()
	if code, body := get(t, port, host); code != http.StatusBadGateway || strings.Contains(body, "HEARTH") {
		t.Errorf("to Hearth: %d %q", code, body)
	}

	// A port the app stopped publishing is not proxied.
	if err := r.p.Apply(ctx, Config{Enabled: true, Port: port, Routes: map[string]int{"cloud": r.backend}}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.targets = map[string][]int{"cloud": {}}
	r.mu.Unlock()
	r.p.mu.Lock()
	r.p.readAt = r.p.readAt.Add(-2 * refresh)
	r.p.mu.Unlock()
	if code, _ := get(t, port, host); code != http.StatusBadGateway {
		t.Errorf("unpublished port: %d", code)
	}

	// Kept: a new hearthd listens again; off closes the listener.
	again := &Proxy{Settings: r.p.Settings, HearthPort: r.p.HearthPort, Targets: r.p.Targets}
	r.p.Stop()
	again.Start(ctx)
	if !again.Status(ctx).Running {
		t.Error("not listening after a restart")
	}
	if err := again.Apply(ctx, Config{Port: port}); err != nil {
		t.Fatal(err)
	}
	if again.Status(ctx).Running {
		t.Error("still listening when off")
	}
	if c, err := net.Dial("tcp4", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		_ = c.Close()
		t.Error("port still open")
	}
}

func TestPortInUse(t *testing.T) {
	r := newRig(t)
	err := r.p.Apply(context.Background(), Config{Enabled: true, Port: r.backend})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("busy port: %v", err)
	}
	if st := r.p.Status(context.Background()); st.Running || st.Error == "" {
		t.Errorf("status %+v", st)
	}
}

func TestActions(t *testing.T) {
	r := newRig(t)
	reg := actions.NewRegistry(nil)
	r.p.Register(reg)
	ctx := context.Background()
	route, _ := reg.Lookup("proxy/route")
	listen, _ := reg.Lookup("proxy/listen")
	for _, req := range []actions.Request{
		{Target: "../x", Params: map[string]string{"port": "1"}},
		{Target: "cloud", Params: map[string]string{"port": "x"}},
		{Target: "cloud", Params: map[string]string{"port": "08080"}},
		{Target: "cloud", Params: map[string]string{"port": "1", "host": "lan"}},
	} {
		if route.Validate(req) == nil {
			t.Errorf("route accepted %+v", req)
		}
	}
	for _, req := range []actions.Request{
		{Target: "cloud", Params: map[string]string{"enabled": "true"}},
		{Target: "proxy", Params: map[string]string{"enabled": "yes"}},
		{Target: "proxy", Params: map[string]string{"port": "80"}},
		{Target: "proxy", Params: map[string]string{"port": strconv.Itoa(r.hearth)}},
		{Target: "proxy", Params: map[string]string{"address": "0.0.0.0"}},
	} {
		if listen.Validate(req) == nil {
			t.Errorf("listen accepted %+v", req)
		}
	}
	// A port the app does not publish is blocked.
	pl, err := route.Plan(ctx, actions.Request{Target: "cloud", Params: map[string]string{"port": "1"}})
	if err != nil || len(pl.Blocked) != 1 {
		t.Errorf("unpublished port: %+v %v", pl, err)
	}
	// Hearth's port, even if the app lists it, is blocked.
	pl, err = route.Plan(ctx, actions.Request{Target: "cloud", Params: map[string]string{"port": strconv.Itoa(r.hearth)}})
	if err != nil || len(pl.Blocked) != 1 {
		t.Errorf("hearth's port: %+v %v", pl, err)
	}
	pl, err = route.Plan(ctx, actions.Request{Target: "cloud", Params: map[string]string{"port": strconv.Itoa(r.backend)}})
	if err != nil || len(pl.Blocked) != 0 {
		t.Fatalf("route: %+v %v", pl, err)
	}
	if res, err := route.Run(ctx, actions.Request{}, pl); err != nil || res.Done != 1 {
		t.Fatalf("run %+v %v", res, err)
	}
	if r.p.Config().Routes["cloud"] != r.backend || r.p.Status(ctx).Running {
		t.Errorf("after route: %+v", r.p.Status(ctx))
	}
	port := freePort(t)
	pl, err = listen.Plan(ctx, actions.Request{Target: "proxy", Params: map[string]string{"enabled": "true", "port": strconv.Itoa(port)}})
	if err != nil || len(pl.Items) != 1 || !strings.Contains(pl.Summary, "on this computer only") {
		t.Fatalf("listen plan %+v %v", pl, err)
	}
	if res, _ := listen.Run(ctx, actions.Request{}, pl); res.Done != 1 || !r.p.Status(ctx).Running {
		t.Errorf("listen run %+v", res)
	}
	pl, _ = route.Plan(ctx, actions.Request{Target: "cloud", Params: map[string]string{"port": "0"}})
	if res, _ := route.Run(ctx, actions.Request{}, pl); res.Done != 1 || len(r.p.Config().Routes) != 0 {
		t.Errorf("route removed %+v %+v", res, r.p.Config())
	}
	// A broker failure blocks a new route rather than guessing.
	r.mu.Lock()
	r.fail = true
	r.mu.Unlock()
	if _, err := route.Plan(ctx, actions.Request{Target: "cloud", Params: map[string]string{"port": strconv.Itoa(r.backend)}}); err == nil {
		t.Error("planned without the apps")
	}
}
