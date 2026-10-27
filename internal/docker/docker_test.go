package docker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// fakeBroker answers calls from a table and records them.
type fakeBroker struct {
	mu      sync.Mutex
	calls   []string
	answers map[string]string // method → JSON result
	errs    map[string]error  // method → error
	stream  func(ctx context.Context, params any, fn func(json.RawMessage) error) error
}

func (f *fakeBroker) Call(_ context.Context, method string, params, out any) error {
	b, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+string(b))
	err, ans := f.errs[method], f.answers[method]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if out == nil || ans == "" {
		return nil
	}
	return json.Unmarshal([]byte(ans), out)
}

func (f *fakeBroker) Stream(ctx context.Context, method string, params any, fn func(json.RawMessage) error) error {
	if method != "docker.containers.logs.follow" || f.stream == nil {
		return broker.ErrNoMethod
	}
	b, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+string(b))
	f.mu.Unlock()
	return f.stream(ctx, params, fn)
}

func (f *fakeBroker) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func inspectAnswer(running bool) string {
	status := "exited"
	if running {
		status = "running"
	}
	b, _ := json.Marshal(map[string]any{"inspect": map[string]any{
		"Id": "abc123", "Name": "/web", "State": map[string]any{"Status": status, "Running": running},
	}, "risks": []string{}})
	return string(b)
}

func opOf(t *testing.T, name string) op {
	for _, o := range ops {
		if o.name == name {
			return o
		}
	}
	t.Fatalf("no op %s", name)
	return op{}
}

func TestPlanAndRun(t *testing.T) {
	ctx := context.Background()
	f := &fakeBroker{answers: map[string]string{"docker.containers.inspect": inspectAnswer(true)}}
	p, err := plan(ctx, f, opOf(t, "stop"), "web")
	if err != nil || len(p.Items) != 1 || p.Items[0] != (actions.Item{ID: "abc123", Name: "web", Detail: "running"}) || p.Confirm != "" {
		t.Fatalf("stop plan %+v %v", p, err)
	}
	res, err := run(ctx, f, opOf(t, "stop"), p)
	if err != nil || res.Done != 1 {
		t.Fatalf("stop %+v %v", res, err)
	}
	if got := f.called(); got[len(got)-1] != `docker.containers.stop {"id":"abc123"}` {
		t.Fatalf("calls %v", got)
	}

	// Removing a running container: the name must be typed, and it is
	// forced; a stopped one is not.
	p, _ = plan(ctx, f, opOf(t, "remove"), "web")
	if p.Confirm != "web" {
		t.Fatalf("remove running %+v", p)
	}
	_, _ = run(ctx, f, opOf(t, "remove"), p)
	f.answers["docker.containers.inspect"] = inspectAnswer(false)
	p2, _ := plan(ctx, f, opOf(t, "remove"), "web")
	if p2.Confirm != "" {
		t.Fatalf("remove stopped %+v", p2)
	}
	_, _ = run(ctx, f, opOf(t, "remove"), p2)
	got := f.called()
	if !slices.Contains(got, `docker.containers.remove {"force":true,"id":"abc123"}`) || got[len(got)-1] != `docker.containers.remove {"force":false,"id":"abc123"}` {
		t.Fatalf("calls %v", got)
	}
}

func TestFailures(t *testing.T) {
	ctx := context.Background()
	f := &fakeBroker{answers: map[string]string{"docker.containers.inspect": inspectAnswer(true)}, errs: map[string]error{}}
	p, _ := plan(ctx, f, opOf(t, "stop"), "web")
	for err, want := range map[error]string{
		broker.ErrUnauthorized: ReasonUnauthorized,
		broker.Errorf(broker.CodeNotFound, "docker: No such container: web"):   ReasonNotFound,
		broker.Errorf(broker.CodeConflict, "docker: container is not running"): ReasonError,
	} {
		f.errs["docker.containers.stop"] = err
		res, rerr := run(ctx, f, opOf(t, "stop"), p)
		if rerr != nil || res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Reason != want {
			t.Errorf("%v: %+v %v", err, res, rerr)
		}
		if want == ReasonError && res.Failed[0].Detail != "container is not running" {
			t.Errorf("detail %q", res.Failed[0].Detail)
		}
	}
	// A broker that cannot serve is an HTTP error, not an item failure.
	f.errs["docker.containers.stop"] = broker.ErrUnavailable
	if _, err := run(ctx, f, opOf(t, "stop"), p); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("unavailable: %v", err)
	}
	// A missing container is blocked at the plan.
	f.errs["docker.containers.inspect"] = broker.Errorf(broker.CodeNotFound, "docker: No such container")
	if p, err := plan(ctx, f, opOf(t, "start"), "gone"); err != nil || len(p.Items) != 0 || p.Blocked[0].Reason != ReasonNotFound {
		t.Errorf("gone %+v %v", p, err)
	}
	f.errs["docker.containers.inspect"] = broker.ErrNotMember
	if _, err := plan(ctx, f, opOf(t, "start"), "web"); err == nil {
		t.Error("not a member: no error")
	}
}

func TestRegisteredActions(t *testing.T) {
	reg := actions.NewRegistry(nopAudit{})
	f := &fakeBroker{answers: map[string]string{"docker.containers.inspect": inspectAnswer(true)}}
	Register(reg, f)
	post := func(action, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/actions/container/"+action, strings.NewReader(body))
		req.SetPathValue("kind", "container")
		req.SetPathValue("action", action)
		rec := httptest.NewRecorder()
		reg.Handle(rec, req)
		return rec
	}
	if rec := post("start", `{"target":"web","params":{}}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"done":1`) {
		t.Fatalf("start %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`{"target":"../etc","params":{}}`, `{"target":"web","params":{"force":"true"}}`, `{"target":"-x","params":{}}`} {
		if rec := post("start", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// remove is destructive: never without a preview token.
	if rec := post("remove", `{"target":"web","params":{}}`); rec.Code == http.StatusOK {
		t.Fatalf("remove ran without a preview: %s", rec.Body.String())
	}
	if slices.ContainsFunc(f.called(), func(c string) bool { return strings.HasPrefix(c, "docker.containers.remove") }) {
		t.Fatal("remove reached the broker")
	}
	rec := post("remove", `{"target":"web","params":{},"preview":true}`)
	var plan struct {
		Token   string `json:"token"`
		Confirm string `json:"confirm"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || plan.Token == "" || plan.Confirm != "web" {
		t.Fatalf("preview %s", rec.Body.String())
	}
	if rec := post("remove", `{"target":"web","params":{},"token":"`+plan.Token+`","confirm":"web"}`); rec.Code != http.StatusOK {
		t.Fatalf("confirmed remove %d %s", rec.Code, rec.Body.String())
	}
}

type nopAudit struct{}

func (nopAudit) Audit(context.Context, store.AuditEntry) error { return nil }

// hub plays api.Hub.
type hub struct {
	mu     sync.Mutex
	topics map[string]int
	msgs   map[string][]json.RawMessage
	watch  map[string]chan struct{}
}

func newHub() *hub {
	return &hub{topics: map[string]int{}, msgs: map[string][]json.RawMessage{}, watch: map[string]chan struct{}{}}
}

func (h *hub) Publish(topic string, v any) error {
	b, err := json.Marshal(v)
	h.mu.Lock()
	h.msgs[topic] = append(h.msgs[topic], b)
	h.mu.Unlock()
	return err
}

func (h *hub) Subscribers(topic string) int { h.mu.Lock(); defer h.mu.Unlock(); return h.topics[topic] }

func (h *hub) Topics(prefix string) map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for t, n := range h.topics {
		if strings.HasPrefix(t, prefix) && n > 0 {
			out[t] = n
		}
	}
	return out
}

func (h *hub) Watch(topic string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan struct{}, 1)
	h.watch[topic] = ch
	return ch
}

func (h *hub) set(topic string, n int) {
	h.mu.Lock()
	h.topics[topic] = n
	key := topic
	if i := strings.IndexByte(topic, ':'); i >= 0 {
		key = topic[:i+1]
	}
	ch := h.watch[key]
	h.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (h *hub) got(topic string) []LogsMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []LogsMsg
	for _, b := range h.msgs[topic] {
		var m LogsMsg
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

// watching waits until something watches topic.
func (h *hub) watching(t *testing.T, topic string) {
	t.Helper()
	waitFor(t, "watch on "+topic, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.watch[topic] != nil })
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

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLogs(t *testing.T) {
	var mu sync.Mutex
	active := map[string]bool{}
	lines := make(chan string, 4)
	f := &fakeBroker{stream: func(ctx context.Context, params any, fn func(json.RawMessage) error) error {
		id := params.(map[string]any)["id"].(string)
		mu.Lock()
		active[id] = true
		mu.Unlock()
		defer func() { mu.Lock(); active[id] = false; mu.Unlock() }()
		if id == "stopped" {
			_ = fn(json.RawMessage(`[{"stream":"stdout","text":"bye"}]`))
			return nil
		}
		_ = fn(json.RawMessage(`[{"stream":"stdout","text":"first"}]`))
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case l := <-lines:
				_ = fn(json.RawMessage(`[{"stream":"stderr","text":"` + l + `"}]`))
			}
		}
	}}
	h := newHub()
	l := NewLogs(f, h, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	isActive := func(id string) bool { mu.Lock(); defer mu.Unlock(); return active[id] }
	h.watching(t, LogsPrefix)

	h.set("docker.logs:web", 1)
	waitFor(t, "first lines", func() bool { return len(h.got("docker.logs:web")) == 1 })
	lines <- "second"
	waitFor(t, "second", func() bool { return len(h.got("docker.logs:web")) == 2 })
	got := h.got("docker.logs:web")
	if !got[0].Reset || got[0].Lines[0].Text != "first" || got[1].Reset || got[1].Lines[0] != (broker.LogLine{Stream: "stderr", Text: "second"}) {
		t.Fatalf("%+v", got)
	}
	if c := f.called(); c[0] != `docker.containers.logs.follow {"id":"web","tail":500}` {
		t.Fatalf("calls %v", c)
	}

	// A second subscriber restarts the follow so it gets the earlier lines.
	h.set("docker.logs:web", 2)
	waitFor(t, "restart", func() bool { g := h.got("docker.logs:web"); return len(g) == 3 && g[2].Reset })

	// A container that stops: lines, then end; it is not followed again.
	h.set("docker.logs:stopped", 1)
	waitFor(t, "end", func() bool {
		g := h.got("docker.logs:stopped")
		return len(g) == 2 && g[1].Kind == "end" && g[1].Error == ""
	})
	h.set("docker.logs:web", 2) // any change re-checks every topic
	time.Sleep(30 * time.Millisecond)
	if n := len(h.got("docker.logs:stopped")); n != 2 {
		t.Fatalf("stopped container followed again: %d messages", n)
	}

	// Bad IDs are refused; the last subscriber leaving stops the follow.
	h.set("docker.logs:../x", 1)
	waitFor(t, "refused", func() bool { g := h.got("docker.logs:../x"); return len(g) == 1 && g[0].Error == "not a container" })
	h.set("docker.logs:web", 0)
	waitFor(t, "stopped", func() bool { return !isActive("web") })
	cancel()
	<-done
}

func TestLogsLimit(t *testing.T) {
	f := &fakeBroker{stream: func(ctx context.Context, _ any, _ func(json.RawMessage) error) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	h := newHub()
	l := NewLogs(f, h, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	h.watching(t, LogsPrefix)
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} {
		h.mu.Lock()
		h.topics["docker.logs:"+id] = 1
		h.mu.Unlock()
	}
	h.set("docker.logs:a1", 1)
	waitFor(t, "one refused", func() bool {
		n := 0
		for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} {
			if g := h.got("docker.logs:" + id); len(g) == 1 && strings.Contains(g[0].Error, "too many") {
				n++
			}
		}
		return n == 1
	})
	cancel()
	<-done
}

func TestPoint(t *testing.T) {
	raw := func(cpu, sys, rx, blk uint64) rawStats {
		var r rawStats
		r.CPUStats.CPUUsage.TotalUsage, r.CPUStats.SystemUsage, r.CPUStats.OnlineCPUs = cpu, sys, 4
		r.MemoryStats.Usage, r.MemoryStats.Limit = 300, 1000
		r.MemoryStats.Stats = map[string]uint64{"inactive_file": 100}
		r.Networks = map[string]struct {
			RxBytes uint64 `json:"rx_bytes"`
			TxBytes uint64 `json:"tx_bytes"`
		}{"eth0": {RxBytes: rx}, "eth1": {RxBytes: rx}}
		r.BlkioStats.IOServiceBytesRecursive = []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		}{{Op: "read", Value: blk}, {Op: "Write", Value: 2 * blk}}
		r.PidsStats.Current = 7
		return r
	}
	t0 := time.Unix(100, 0)
	p, c := point(raw(1000, 10000, 0, 0), t0, counters{})
	if p.CPU != 0 || p.Mem != 200 || p.MemLimit != 1000 || p.Pids != 7 || p.NetRx != 0 {
		t.Fatalf("first %+v", p)
	}
	// 2 s later: 500 of 10000 system ns on 4 CPUs is 20% of one core.
	p, _ = point(raw(1500, 20000, 1000, 4000), t0.Add(2*time.Second), c)
	if p.CPU != 20 || p.NetRx != 1000 || p.BlkRead != 2000 || p.BlkWrite != 4000 {
		t.Fatalf("second %+v", p)
	}
	// Counters that went back (a restart) give no rate.
	p, _ = point(raw(10, 30000, 0, 0), t0.Add(4*time.Second), c)
	if p.CPU != 0 || p.NetRx != 0 {
		t.Fatalf("reset %+v", p)
	}
}

func TestStats(t *testing.T) {
	f := &fakeBroker{answers: map[string]string{"docker.containers.list": `[{"Id":"c1"},{"Id":"c2"}]`}}
	f.answers["docker.containers.stats"] = `{"cpu_stats":{"cpu_usage":{"total_usage":0},"system_cpu_usage":0,"online_cpus":1},"memory_stats":{"usage":42}}`
	h := newHub()
	s := NewStats(f, h, quiet(), 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	h.watching(t, StatsTopic)
	time.Sleep(40 * time.Millisecond)
	if len(f.called()) != 0 {
		t.Fatal("sampled without subscribers")
	}
	h.set(StatsTopic, 1)
	waitFor(t, "samples", func() bool { return len(s.History()["c2"]) >= 3 })
	h.mu.Lock()
	first := h.msgs[StatsTopic][0]
	h.mu.Unlock()
	if !strings.Contains(string(first), `"kind":"history"`) {
		t.Fatalf("first message %s", first)
	}
	if p := s.History()["c1"][0]; p.Mem != 42 {
		t.Fatalf("%+v", p)
	}
	h.set(StatsTopic, 0)
	time.Sleep(30 * time.Millisecond)
	n := len(f.called())
	time.Sleep(60 * time.Millisecond)
	if len(f.called()) != n {
		t.Fatal("still sampling without subscribers")
	}
	if !slices.Contains(f.called(), `docker.containers.list {"all":false}`) {
		t.Fatalf("calls %v", f.called())
	}
	cancel()
	<-done
}

func TestHandleInspect(t *testing.T) {
	f := &fakeBroker{answers: map[string]string{"docker.containers.inspect": inspectAnswer(true)}, errs: map[string]error{}}
	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/docker/containers/"+id, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		HandleInspect(f)(rec, req)
		return rec
	}
	if rec := get("web"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Name":"/web"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := get("-rf"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id %d", rec.Code)
	}
	f.errs["docker.containers.inspect"] = broker.Errorf(broker.CodeNotFound, "docker: No such container: x")
	if rec := get("x"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No such container") {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}
	f.errs["docker.containers.inspect"] = broker.ErrUnavailable
	if rec := get("x"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no broker %d", rec.Code)
	}
}
