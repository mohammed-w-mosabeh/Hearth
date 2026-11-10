package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

type memAudit struct {
	mu   sync.Mutex
	recs []AuditRecord
}

func (m *memAudit) Record(r AuditRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
}

func (m *memAudit) results() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.recs {
		out = append(out, r.Method+" "+r.Result)
	}
	return out
}

type authority struct {
	allow bool
	err   error
	mu    sync.Mutex
	asked []string
}

func (a *authority) Check(_ context.Context, c Caller, action string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, action+" pid="+strconv.Itoa(int(c.PID)))
	return a.allow, a.err
}

type harness struct {
	t     *testing.T
	sock  string
	audit *memAudit
	auth  *authority
	ran   *int
	stop  context.CancelFunc
	done  chan error
}

// start serves the base methods plus a privileged "test.change" on a
// temporary socket. The caller is faked as uid (member or not).
func start(t *testing.T, member bool, cfgMod func(*Config)) *harness {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, sock: sock, audit: &memAudit{}, auth: &authority{}, ran: new(int), done: make(chan error, 1)}
	var srv *Server
	methods := append(BaseMethods("test", func() []string { return srv.Names() }), Method{
		Name: "test.change", Privileged: true,
		Handle: func(context.Context, Caller, json.RawMessage) (any, error) { *h.ran++; return "changed", nil },
	}, Method{
		Name:   "test.fail",
		Handle: func(context.Context, Caller, json.RawMessage) (any, error) { return nil, errors.New("disk on fire") },
	})
	cfg := Config{
		Methods: methods, InGroup: func(uint32) bool { return member }, Authority: h.auth, Audit: h.audit,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PeerCred: func(net.Conn) (Caller, error) { return Caller{UID: 1000, PID: int32(os.Getpid())}, nil }, // #nosec G115 -- a pid
	}
	if cfgMod != nil {
		cfgMod(&cfg)
	}
	srv, err = NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	go func() { h.done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-h.done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return h
}

// raw sends lines on one connection and returns the response lines.
func (h *harness) raw(lines ...string) []Response {
	h.t.Helper()
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	var out []Response
	for _, l := range lines {
		// A refused caller's answer may already be waiting when the write
		// fails (the broker closed the connection): read it anyway.
		_, werr := io.WriteString(conn, l+"\n")
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			h.t.Fatalf("bad response %q", line)
		}
		out = append(out, resp)
		if werr != nil {
			break
		}
	}
	return out
}

func (h *harness) client() *Client { return &Client{Path: h.sock} }

func TestPingAndAudit(t *testing.T) {
	h := start(t, true, nil)
	var p PingResult
	if err := h.client().Call(context.Background(), "broker.ping", nil, &p); err != nil {
		t.Fatal(err)
	}
	if p.UID != 1000 || p.Version != "test" || strings.Join(p.Methods, ",") != "broker.authorize,broker.ping,test.change,test.fail" {
		t.Fatalf("%+v", p)
	}
	if got := h.audit.results(); len(got) != 1 || got[0] != "broker.ping ok" {
		t.Fatalf("audit %v", got)
	}
}

func TestNonMemberRefused(t *testing.T) {
	h := start(t, false, nil)
	err := h.client().Call(context.Background(), "broker.ping", nil, nil)
	if !errors.Is(err, ErrNotMember) {
		t.Fatalf("err %v", err)
	}
	// The connection is closed: a second line gets nothing.
	if resp := h.raw(`{"id":1,"method":"broker.ping"}`, `{"id":2,"method":"broker.ping"}`); len(resp) != 1 || resp[0].Error.Code != CodeNotMember {
		t.Fatalf("%+v", resp)
	}
	if got := h.audit.results(); len(got) != 2 || !strings.Contains(got[0], "refused: not in group hearth") {
		t.Fatalf("audit %v", got)
	}
}

func TestUnknownAndMalformed(t *testing.T) {
	h := start(t, true, nil)
	resp := h.raw(
		`{"id":1,"method":"exec","params":{"cmd":"/bin/sh"}}`,
		`{"id":2,"method":"broker.ping","extra":true}`,
		`not json`,
		`{"id":3,"method":"broker.ping"} {"id":4}`,
		`{"id":5,"method":"broker.ping","params":{"x":1}}`,
		`{"id":6,"method":"test.fail"}`,
		`{"id":7,"method":"broker.ping"}`,
	)
	want := []int{CodeNoMethod, CodeParse, CodeParse, CodeParse, CodeBadParams, CodeFailed, 0}
	if len(resp) != len(want) {
		t.Fatalf("%d responses: %+v", len(resp), resp)
	}
	for i, w := range want {
		got := 0
		if resp[i].Error != nil {
			got = resp[i].Error.Code
		}
		if got != w {
			t.Errorf("line %d: code %d, want %d (%+v)", i, got, w, resp[i].Error)
		}
	}
	if resp[0].ID != 1 || resp[5].Error.Message != "disk on fire" {
		t.Errorf("ids/messages %+v", resp)
	}
	audit := strings.Join(h.audit.results(), "\n")
	for _, w := range []string{"exec refused: unknown method", "(malformed) refused: not a request", "test.fail error: disk on fire"} {
		if !strings.Contains(audit, w) {
			t.Errorf("audit lacks %q:\n%s", w, audit)
		}
	}
}

func TestPrivilegedNeedsPolkit(t *testing.T) {
	h := start(t, true, nil)
	c := h.client()
	ctx := context.Background()
	if err := c.Call(ctx, "test.change", nil, nil); !errors.Is(err, ErrUnauthorized) || *h.ran != 0 {
		t.Fatalf("unauthorized ran: %v %d", err, *h.ran)
	}
	h.auth.err = errors.New("polkitd is not running")
	if err := c.Call(ctx, "test.change", nil, nil); !errors.Is(err, ErrUnauthorized) || *h.ran != 0 {
		t.Fatalf("polkit error did not fail closed: %v", err)
	}
	h.auth.err, h.auth.allow = nil, true
	var out string
	if err := c.Call(ctx, "test.change", nil, &out); err != nil || out != "changed" || *h.ran != 1 {
		t.Fatalf("authorized: %v %q", err, out)
	}
	if err := c.Call(ctx, "broker.authorize", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.auth.asked) != 4 || !strings.HasPrefix(h.auth.asked[0], ActionManage+" pid=") {
		t.Errorf("asked %v", h.auth.asked)
	}
	// Unprivileged methods never ask.
	_ = c.Call(ctx, "broker.ping", nil, nil)
	if len(h.auth.asked) != 4 {
		t.Error("ping asked polkit")
	}
	audit := strings.Join(h.audit.results(), "\n")
	if !strings.Contains(audit, "test.change refused: not authorized") || !strings.Contains(audit, "test.change refused: authorization failed: polkitd is not running") {
		t.Errorf("audit:\n%s", audit)
	}
}

// A privileged call whose params are refused anyway raises no polkit
// dialog: Check runs first.
func TestCheckBeforePolkit(t *testing.T) {
	h := start(t, true, nil)
	if err := h.client().Call(context.Background(), "broker.authorize", map[string]int{"x": 1}, nil); code(err) != CodeBadParams {
		t.Fatalf("got %v", err)
	}
	if len(h.auth.asked) != 0 {
		t.Fatalf("polkit asked: %v", h.auth.asked)
	}
	if audit := strings.Join(h.audit.results(), "\n"); !strings.Contains(audit, "broker.authorize error: ") {
		t.Errorf("audit:\n%s", audit)
	}
}

// A caller that connects and writes half a request loses its slot after
// RequestTimeout, not after the 60 s idle time.
func TestRequestTimeout(t *testing.T) {
	h := start(t, true, func(c *Config) { c.RequestTimeout = 100 * time.Millisecond })
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, `{"id":1,"method":"broker.pi`)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) || time.Since(start) > 2*time.Second {
		t.Fatalf("still open: %v after %s", err, time.Since(start))
	}
}

func TestOversizedLine(t *testing.T) {
	h := start(t, true, nil)
	resp := h.raw(`{"id":1,"method":"broker.ping","params":"` + strings.Repeat("a", MaxLine) + `"}`)
	if len(resp) != 1 || resp[0].Error.Code != CodeInvalid {
		t.Fatalf("%+v", resp)
	}
}

func TestIdleExit(t *testing.T) {
	h := start(t, true, func(c *Config) { c.Idle = 100 * time.Millisecond })
	_ = h.client().Call(context.Background(), "broker.ping", nil, nil)
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- nil // for the cleanup
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not exit when idle")
	}
	if err := h.client().Call(context.Background(), "broker.ping", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("after exit: %v", err)
	}
}

func TestIdleWaitsForBusyConnection(t *testing.T) {
	h := start(t, true, func(c *Config) { c.Idle = 150 * time.Millisecond })
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	// Busy for four idle periods: the broker must stay.
	for i := range 12 {
		if _, err := io.WriteString(conn, `{"id":`+strconv.Itoa(i)+`,"method":"broker.ping"}`+"\n"); err != nil {
			t.Fatalf("broker went away while busy: %v", err)
		}
		if _, err := r.ReadBytes('\n'); err != nil {
			t.Fatalf("broker went away while busy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case err := <-h.done:
		t.Fatalf("exited with a busy connection (%v)", err)
	default:
	}
	_ = conn.Close()
	select {
	case err := <-h.done:
		h.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("did not exit once the connection closed")
	}
}

func TestRealPeerCred(t *testing.T) {
	h := start(t, true, func(c *Config) { c.PeerCred = nil })
	var p PingResult
	if err := h.client().Call(context.Background(), "broker.ping", nil, &p); err != nil {
		t.Fatal(err)
	}
	if p.UID != uint32(os.Getuid()) { // #nosec G115 -- test
		t.Fatalf("uid %d", p.UID)
	}
	if startTime("/proc", int32(os.Getpid())) == 0 { // #nosec G115 -- test
		t.Fatal("no start time for self")
	}
}

func TestClientErrors(t *testing.T) {
	c := &Client{Path: filepath.Join(t.TempDir(), "missing.sock")}
	if err := c.Call(context.Background(), "broker.ping", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing socket: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	h := start(t, true, nil)
	if err := os.Chmod(h.sock, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.client().Call(context.Background(), "broker.ping", nil, nil); !errors.Is(err, ErrNotMember) {
		t.Fatalf("no permission: %v", err)
	}
	rec := httptest.NewRecorder()
	h.client().HandleStatus(rec, httptest.NewRequest("GET", "/api/v1/broker/status", nil))
	if !strings.Contains(rec.Body.String(), `"reason":"not-member"`) {
		t.Fatalf("status %s", rec.Body.String())
	}
}

func TestStatusOK(t *testing.T) {
	h := start(t, true, nil)
	rec := httptest.NewRecorder()
	h.client().HandleStatus(rec, httptest.NewRequest("GET", "/api/v1/broker/status", nil))
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Available || !st.Member || st.Version != "test" {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestNewServerRejectsBadTables(t *testing.T) {
	base := Config{InGroup: func(uint32) bool { return true }, Authority: &authority{}, Audit: &memAudit{}}
	dup := base
	dup.Methods = []Method{{Name: "a", Handle: func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }}, {Name: "a", Handle: func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }}}
	if _, err := NewServer(dup); err == nil {
		t.Error("duplicate accepted")
	}
	if _, err := NewServer(Config{}); err == nil {
		t.Error("missing deps accepted")
	}
}

func TestGroupMember(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skip(err)
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	if !GroupMember(g.Name)(uint32(uid)) {
		t.Errorf("not a member of own group %s", g.Name)
	}
	if GroupMember("no-such-group-hearth-test")(uint32(uid)) {
		t.Error("member of a missing group")
	}
}

func TestFileAudit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a := &FileAudit{Path: p, MaxBytes: 200}
	for i := range 5 {
		a.Record(AuditRecord{TS: int64(i), UID: 1000, Method: "broker.ping", Result: "ok"})
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("not rotated: %v", err)
	}
	b, _ := os.ReadFile(p) // #nosec G304 -- a test file
	if !strings.Contains(string(b), `"method":"broker.ping","result":"ok"`) {
		t.Fatalf("%s", b)
	}
}

// streamMethods adds "test.stream", which sends n items and then waits
// for the caller to leave (left is closed the first time), and
// "test.change.stream", privileged.
func streamMethods(n int, left chan<- struct{}) func(*Config) {
	var once sync.Once
	return func(c *Config) {
		run := func(ctx context.Context, _ Caller, _ json.RawMessage, send func(any) error) error {
			for i := range n {
				if err := send(i); err != nil {
					return err
				}
			}
			<-ctx.Done()
			once.Do(func() { close(left) })
			return ctx.Err()
		}
		c.Methods = append(c.Methods,
			Method{Name: "test.stream", Stream: run},
			Method{Name: "test.change.stream", Privileged: true, Stream: run},
		)
	}
}

func TestStream(t *testing.T) {
	left := make(chan struct{})
	h := start(t, true, streamMethods(3, left))
	ctx, cancel := context.WithCancel(context.Background())
	var got []int
	err := h.client().Stream(ctx, "test.stream", nil, func(raw json.RawMessage) error {
		var i int
		if err := json.Unmarshal(raw, &i); err != nil {
			return err
		}
		if got = append(got, i); len(got) == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(got) != 3 || got[2] != 2 {
		t.Fatalf("err %v, got %v", err, got)
	}
	select {
	case <-left: // closing the connection ends the method
	case <-time.After(3 * time.Second):
		t.Fatal("stream method still running after the caller left")
	}
	want := []string{"test.stream ok: stream", "test.stream ended: closed"}
	for deadline := time.Now().Add(3 * time.Second); !slices.Equal(h.audit.results(), want); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("audit %v", h.audit.results())
		}
	}
	// A stream is not a call: Call refuses its first item.
	if err := h.client().Call(context.Background(), "test.stream", nil, nil); err == nil {
		t.Fatal("Call accepted a stream")
	}
}

func TestStreamEndsAndFails(t *testing.T) {
	h := start(t, true, func(c *Config) {
		c.Methods = append(c.Methods,
			Method{Name: "test.done", Stream: func(_ context.Context, _ Caller, _ json.RawMessage, send func(any) error) error {
				return send("only")
			}},
			Method{Name: "test.broken", Stream: func(context.Context, Caller, json.RawMessage, func(any) error) error {
				return Errorf(CodeFailed, "pipe burst")
			}},
		)
	})
	n := 0
	if err := h.client().Stream(context.Background(), "test.done", nil, func(json.RawMessage) error { n++; return nil }); err != nil || n != 1 {
		t.Fatalf("err %v, n %d", err, n)
	}
	err := h.client().Stream(context.Background(), "test.broken", nil, func(json.RawMessage) error { return nil })
	if code(err) != CodeFailed || !strings.Contains(err.Error(), "pipe burst") {
		t.Fatalf("err %v", err)
	}
	if err := h.client().Stream(context.Background(), "no.such", nil, func(json.RawMessage) error { return nil }); code(err) != CodeNoMethod {
		t.Fatalf("unknown: %v", err)
	}
}

func TestStreamRefusals(t *testing.T) {
	left := make(chan struct{})
	h := start(t, false, streamMethods(1, left))
	if err := h.client().Stream(context.Background(), "test.stream", nil, func(json.RawMessage) error { return nil }); !errors.Is(err, ErrNotMember) {
		t.Fatalf("non-member: %v", err)
	}
	h = start(t, true, streamMethods(1, left))
	if err := h.client().Stream(context.Background(), "test.change.stream", nil, func(json.RawMessage) error { return nil }); code(err) != CodeUnauthorized {
		t.Fatalf("unauthorized: %v", err)
	}
	bad := Config{InGroup: func(uint32) bool { return true }, Authority: &authority{}, Audit: &memAudit{}, Methods: []Method{{
		Name:   "both",
		Handle: func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil },
		Stream: func(context.Context, Caller, json.RawMessage, func(any) error) error { return nil },
	}}}
	if _, err := NewServer(bad); err == nil {
		t.Fatal("method with Handle and Stream accepted")
	}
}

func TestStreamKeepsBrokerAwake(t *testing.T) {
	left := make(chan struct{})
	h := start(t, true, func(c *Config) { c.Idle = 100 * time.Millisecond; streamMethods(0, left)(c) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.client().Stream(ctx, "test.stream", nil, func(json.RawMessage) error { return nil }) }()
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-h.done:
		t.Fatalf("exited during a stream (%v)", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stream ended with %v", err)
	}
	select {
	case err := <-h.done:
		h.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("did not exit once the stream ended")
	}
}

func TestSession(t *testing.T) {
	h := start(t, true, func(c *Config) {
		c.Methods = append(c.Methods, Method{Name: "test.echo", Session: func(ctx context.Context, _ Caller, _ json.RawMessage, in <-chan json.RawMessage, send func(any) error) error {
			for line := range in {
				var s string
				if json.Unmarshal(line, &s) != nil {
					return Errorf(CodeBadParams, "not a string")
				}
				if s == "bye" {
					return nil
				}
				if err := send(strings.ToUpper(s)); err != nil {
					return err
				}
			}
			return ctx.Err()
		}}, Method{Name: "test.priv", Privileged: true, Session: func(context.Context, Caller, json.RawMessage, <-chan json.RawMessage, func(any) error) error {
			t.Error("unauthorized session ran")
			return nil
		}})
	})
	s, err := h.client().Open(context.Background(), "test.echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"a", "bc"} {
		if err := s.Send(w); err != nil {
			t.Fatal(err)
		}
		raw, err := s.Recv()
		if err != nil || string(raw) != `"`+strings.ToUpper(w)+`"` {
			t.Fatalf("%s: %s %v", w, raw, err)
		}
	}
	_ = s.Send("bye")
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
	_ = s.Close()

	s, _ = h.client().Open(context.Background(), "test.echo", nil)
	_ = s.Send(42)
	if _, err := s.Recv(); code(err) != CodeBadParams {
		t.Fatalf("bad line: %v", err)
	}
	_ = s.Close()

	s, _ = h.client().Open(context.Background(), "test.priv", nil)
	if _, err := s.Recv(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("privileged: %v", err)
	}
	_ = s.Close()
	if err := s.Send(strings.Repeat("x", MaxLine)); err == nil {
		t.Fatal("oversized line sent")
	}
}

// Unattended work (an automation, S15d) never reaches polkit: a privileged
// method is refused before it is asked, even when polkit would allow it.
func TestUnattendedNeverAsksPolkit(t *testing.T) {
	h := start(t, true, nil)
	h.auth.allow = true
	ctx := unattended.With(context.Background())
	if err := h.client().Call(ctx, "test.change", nil, nil); !errors.Is(err, ErrUnauthorized) || *h.ran != 0 {
		t.Fatalf("an unattended privileged call ran: %v", err)
	}
	if len(h.auth.asked) != 0 {
		t.Fatalf("polkit asked: %v", h.auth.asked)
	}
	// Unprivileged methods still answer.
	if err := h.client().Call(ctx, "broker.ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	if audit := strings.Join(h.audit.results(), "\n"); !strings.Contains(audit, "refused: unattended") {
		t.Errorf("audit:\n%s", audit)
	}
	// The same call, attended, reaches polkit.
	if err := h.client().Call(context.Background(), "test.change", nil, nil); err != nil || len(h.auth.asked) != 1 {
		t.Fatalf("%v %v", err, h.auth.asked)
	}
}
