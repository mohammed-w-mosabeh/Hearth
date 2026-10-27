package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// fakeSession plays the broker's exec session: ready (or openErr), then
// echoes input as output until "exit".
type fakeSession struct {
	mu     sync.Mutex
	sent   []string
	out    chan json.RawMessage
	err    error // first Recv
	closed bool
}

func (s *fakeSession) Recv() (json.RawMessage, error) {
	if s.err != nil {
		return nil, s.err
	}
	raw, ok := <-s.out
	if !ok {
		return nil, io.EOF
	}
	return raw, nil
}

func (s *fakeSession) Send(v any) error {
	b, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, string(b))
	var in broker.ExecIn
	_ = json.Unmarshal(b, &in)
	if string(in.In) == "exit\n" {
		close(s.out)
	} else if len(in.In) > 0 {
		o, _ := json.Marshal(broker.ExecOut{Out: append([]byte("> "), in.In...)})
		s.out <- o
	}
	return nil
}

func (s *fakeSession) Close() error { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }

type auditLog struct {
	mu sync.Mutex
	r  []string
}

func (a *auditLog) add(_ context.Context, e store.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.r = append(a.r, e.Action+" "+e.Target+" "+e.Result)
	return nil
}

func (a *auditLog) get() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.r...)
}

func execServer(t *testing.T, s *fakeSession, openErr error) (*httptest.Server, *auditLog, *[]any) {
	t.Helper()
	al := &auditLog{}
	var params []any
	mux := http.NewServeMux()
	mux.Handle("GET /exec/{id}", HandleExec(ExecConfig{
		Open: func(_ context.Context, method string, p any) (ExecSession, error) {
			params = append(params, method, p)
			if openErr != nil {
				return nil, openErr
			}
			return s, nil
		},
		Audit: al.add, Log: quiet(),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, al, &params
}

func dial(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, &websocket.DialOptions{Subprotocols: []string{auth.Subprotocol}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func readMsg(t *testing.T, c *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, string(b)
}

func TestExec(t *testing.T) {
	s := &fakeSession{out: make(chan json.RawMessage, 8)}
	s.out <- json.RawMessage(`{"ready":true}`)
	srv, al, params := execServer(t, s, nil)
	c := dial(t, srv, "/exec/web?cols=100&rows=30")
	defer func() { _ = c.CloseNow() }()
	ctx := context.Background()

	if _, m := readMsg(t, c); m != `{"state":"auth"}` {
		t.Fatalf("%s", m)
	}
	if _, m := readMsg(t, c); m != `{"state":"ready"}` {
		t.Fatalf("%s", m)
	}
	if p, _ := json.Marshal((*params)[1]); string(p) != `{"cols":100,"id":"web","rows":30}` {
		t.Fatalf("params %s", p)
	}
	_ = c.Write(ctx, websocket.MessageBinary, []byte("ls\n"))
	if typ, m := readMsg(t, c); typ != websocket.MessageBinary || m != "> ls\n" {
		t.Fatalf("%v %q", typ, m)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"resize":[90,20]}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"in":"ZXZpbA=="}`)) // text frames carry control only
	_ = c.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
	if _, m := readMsg(t, c); m != `{"state":"exit"}` {
		t.Fatalf("%s", m)
	}
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close: %v", err)
	}
	s.mu.Lock()
	sent := strings.Join(s.sent, " ")
	s.mu.Unlock()
	if sent != `{"in":"bHMK"} {"resize":[90,20]} {"in":"ZXhpdAo="}` {
		t.Fatalf("sent %s", sent)
	}
	waitFor(t, "audit", func() bool { return len(al.get()) == 2 })
	if got := al.get(); got[0] != "container/exec web ok: shell opened" || got[1] != "container/exec web ended" {
		t.Fatalf("audit %v", got)
	}
}

func TestExecRefused(t *testing.T) {
	s := &fakeSession{err: broker.ErrUnauthorized}
	srv, al, _ := execServer(t, s, nil)
	c := dial(t, srv, "/exec/web")
	defer func() { _ = c.CloseNow() }()
	readMsg(t, c) // auth
	if _, m := readMsg(t, c); !strings.Contains(m, `"state":"exit"`) || !strings.Contains(m, "not authorized") {
		t.Fatalf("%s", m)
	}
	waitFor(t, "audit", func() bool { return len(al.get()) == 1 })
	if !strings.HasPrefix(al.get()[0], "container/exec web refused: not authorized") {
		t.Fatalf("audit %v", al.get())
	}

	srv, _, _ = execServer(t, nil, broker.ErrUnavailable)
	c = dial(t, srv, "/exec/web")
	readMsg(t, c)
	if _, m := readMsg(t, c); !strings.Contains(m, "hearth-broker is not available") {
		t.Fatalf("%s", m)
	}
}

func TestExecBadRequests(t *testing.T) {
	srv, _, params := execServer(t, &fakeSession{}, nil)
	for _, path := range []string{"/exec/-rf", "/exec/web?cols=0", "/exec/web?rows=5000", "/exec/web?cols=x"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	// Without the subprotocol the connection is refused before the broker.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/exec/web", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		_, _, err = c.Read(ctx)
		_ = c.CloseNow()
	}
	if err == nil || len(*params) != 0 {
		t.Fatalf("no subprotocol: %v, opened %v", err, *params)
	}
}
