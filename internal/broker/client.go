package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrUnavailable means the broker is not installed or not running.
var ErrUnavailable = errors.New("broker: not available")

// Client calls the broker from hearthd. Each call is its own connection,
// so an idle hearthd holds none (and the broker can exit).
type Client struct {
	Path string // default SocketPath
	ids  atomic.Int64
}

// dial connects and sends one request.
func (c *Client) dial(ctx context.Context, method string, params any) (net.Conn, Request, error) {
	path := c.Path
	if path == "" {
		path = SocketPath
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	switch {
	case errors.Is(err, syscall.EACCES) || errors.Is(err, fs.ErrPermission):
		return nil, Request{}, ErrNotMember
	case err != nil:
		return nil, Request{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req := Request{ID: c.ids.Add(1), Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			_ = conn.Close()
			return nil, Request{}, err
		}
	}
	// A refused caller gets its answer and the connection closed at once,
	// possibly before the request is written: the answer is read anyway,
	// and a write error only counts when there is none.
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if werr := json.NewEncoder(conn).Encode(req); werr != nil {
		req.ID = -1 // no response can match
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return conn, req, nil
}

// readResponse reads one response line of at most MaxResponse bytes.
func readResponse(r *bufio.Reader) (Response, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxResponse {
			return Response{}, errors.New("broker: response too large")
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return Response{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		break
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("broker: bad response: %w", err)
	}
	return resp, nil
}

// Call runs method with params and decodes its result into out (if not
// nil). Errors: ErrUnavailable; ErrNotMember when the user is not in the
// hearth group (the socket refuses the connection, or the broker does);
// otherwise the broker's *Error.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	conn, req, err := c.dial(ctx, method, params)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(4 * time.Minute)) // a polkit dialog can take a while
	}
	resp, err := readResponse(bufio.NewReaderSize(conn, 64<<10))
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.ID != req.ID || resp.More {
		return errors.New("broker: response to another request")
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Stream runs a streaming method, calling fn with each item until the
// stream ends (nil), fails, fn returns an error, or ctx ends (ctx.Err()).
func (c *Client) Stream(ctx context.Context, method string, params any, fn func(json.RawMessage) error) error {
	conn, req, err := c.dial(ctx, method, params)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		resp, err := readResponse(r)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if resp.Error != nil {
			return resp.Error
		}
		if resp.ID != req.ID {
			return errors.New("broker: response to another request")
		}
		if !resp.More {
			return nil
		}
		if err := fn(resp.Result); err != nil {
			return err
		}
	}
}

// Session is an open session method (Client.Open): items from the
// broker with Recv, lines to it with Send.
type Session struct {
	conn net.Conn
	r    *bufio.Reader
	id   int64
	stop func() bool
	wmu  sync.Mutex
}

// Open starts a session method. A privileged one is authorized before
// its first item, so the first Recv may wait for a polkit dialog. The
// session ends when ctx does or Close is called.
func (c *Client) Open(ctx context.Context, method string, params any) (*Session, error) {
	conn, req, err := c.dial(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return &Session{
		conn: conn, r: bufio.NewReaderSize(conn, 64<<10), id: req.ID,
		stop: context.AfterFunc(ctx, func() { _ = conn.Close() }),
	}, nil
}

// Recv returns the next item: io.EOF when the session ended, the
// broker's *Error when it failed.
func (s *Session) Recv() (json.RawMessage, error) {
	resp, err := readResponse(s.r)
	switch {
	case err != nil:
		return nil, err
	case resp.Error != nil:
		return nil, resp.Error
	case resp.ID != s.id:
		return nil, errors.New("broker: response to another request")
	case !resp.More:
		return nil, io.EOF
	}
	return resp.Result, nil
}

// Send sends v as one line; it may be called alongside Recv.
func (s *Session) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) >= MaxLine {
		return errors.New("broker: message too large")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = s.conn.Write(append(b, '\n'))
	return err
}

// Close ends the session.
func (s *Session) Close() error {
	s.stop()
	return s.conn.Close()
}

// Status is GET /api/v1/broker/status.
type Status struct {
	Available bool     `json:"available"`
	Member    bool     `json:"member"` // the user is in the hearth group
	Version   string   `json:"version,omitempty"`
	Methods   []string `json:"methods,omitempty"`
	Reason    string   `json:"reason,omitempty"` // "not-installed", "not-member", "error"
}

// HandleStatus serves GET /api/v1/broker/status.
func (c *Client) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var st Status
	var ping PingResult
	switch err := c.Call(ctx, "broker.ping", nil, &ping); {
	case err == nil:
		st = Status{Available: true, Member: true, Version: ping.Version, Methods: ping.Methods}
	case errors.Is(err, ErrNotMember):
		st = Status{Available: true, Reason: "not-member"}
	case errors.Is(err, ErrUnavailable):
		st = Status{Reason: "not-installed"}
	default:
		st = Status{Reason: "error"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}
