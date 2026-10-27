package broker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Caller is the process on the other end of a connection.
type Caller struct {
	UID   uint32
	GID   uint32
	PID   int32
	Start uint64 // process start time (clock ticks after boot), for polkit
}

// Method is one allowlisted method.
type Method struct {
	Name string
	// Privileged methods are authorized by polkit (Action, default
	// ActionManage) for the calling process before Handle runs.
	Privileged bool
	Action     string
	// Timeout bounds the call (polkit dialog included); default
	// Config.CallTimeout.
	Timeout time.Duration
	// Handle runs the method. Params are the raw "params"; the method
	// must validate them and reject anything unexpected.
	Handle func(ctx context.Context, c Caller, params json.RawMessage) (any, error)
	// Stream, instead of Handle, runs a streaming method: it calls send
	// for each item until it returns or ctx ends (the caller closed the
	// connection). Streams have no timeout.
	Stream func(ctx context.Context, c Caller, params json.RawMessage, send func(any) error) error
	// Session, instead of Handle or Stream, is a stream that also reads:
	// each line the caller sends after the request arrives on in (closed
	// when the caller closes the connection). Lines are bounded by
	// MaxLine and are not audited; the method must validate each.
	Session func(ctx context.Context, c Caller, params json.RawMessage, in <-chan json.RawMessage, send func(any) error) error
}

// Authority decides privileged calls (polkit in production).
type Authority interface {
	Check(ctx context.Context, c Caller, action string) (bool, error)
}

// Auditor records every call.
type Auditor interface {
	Record(r AuditRecord)
}

// AuditRecord is one audited call or refused connection.
type AuditRecord struct {
	Time       time.Time `json:"-"`
	TS         int64     `json:"ts"` // Unix ms
	UID        uint32    `json:"uid"`
	PID        int32     `json:"pid"`
	Method     string    `json:"method"`
	ParamsHash string    `json:"params_hash,omitempty"` // SHA-256 of the raw params; params are never stored
	Result     string    `json:"result"`
}

// Config configures a Server.
type Config struct {
	Methods   []Method
	InGroup   func(uid uint32) bool
	Authority Authority
	Audit     Auditor
	Log       *slog.Logger
	// Idle ends Serve once no connection has been open for this long;
	// default 60 s (SECURITY S11). The socket starts the broker again.
	Idle time.Duration
	// MaxConns bounds concurrent connections; default 16.
	MaxConns int
	// CallTimeout bounds one call, polkit dialog included; default 3 min.
	CallTimeout time.Duration
	// ProcRoot is where caller start times are read; default /proc.
	ProcRoot string
	// PeerCred reads the caller of conn; default SO_PEERCRED.
	PeerCred func(conn net.Conn) (Caller, error)
}

// Server serves broker connections.
type Server struct {
	cfg     Config
	methods map[string]Method

	mu     sync.Mutex
	active int
	last   time.Time
	slots  chan struct{}
}

// NewServer checks the method table and returns a Server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.InGroup == nil || cfg.Authority == nil || cfg.Audit == nil {
		return nil, errors.New("broker: InGroup, Authority and Audit are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Idle == 0 {
		cfg.Idle = 60 * time.Second
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 16
	}
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = 3 * time.Minute
	}
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.PeerCred == nil {
		cfg.PeerCred = peerCred
	}
	s := &Server{cfg: cfg, methods: map[string]Method{}, slots: make(chan struct{}, cfg.MaxConns), last: time.Now()}
	for _, m := range cfg.Methods {
		kinds := 0
		for _, set := range []bool{m.Handle != nil, m.Stream != nil, m.Session != nil} {
			if set {
				kinds++
			}
		}
		if m.Name == "" || kinds != 1 {
			return nil, fmt.Errorf("broker: bad method %q", m.Name)
		}
		if _, dup := s.methods[m.Name]; dup {
			return nil, fmt.Errorf("broker: duplicate method %q", m.Name)
		}
		if m.Privileged && m.Action == "" {
			m.Action = ActionManage
		}
		s.methods[m.Name] = m
	}
	return s, nil
}

// Names lists the allowlisted methods.
func (s *Server) Names() []string {
	out := make([]string, 0, len(s.methods))
	for n := range s.methods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Serve accepts connections until ln is closed or the server has been idle
// for Config.Idle, and waits for open connections to finish.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	idle := make(chan struct{})
	go func() {
		tick := time.NewTicker(s.cfg.Idle / 10)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = ln.Close()
				return
			case <-tick.C:
				s.mu.Lock()
				done := s.active == 0 && time.Since(s.last) >= s.cfg.Idle
				s.mu.Unlock()
				if done {
					close(idle)
					_ = ln.Close()
					return
				}
			}
		}
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-idle:
				s.cfg.Log.Info("broker: idle, exiting")
				return nil
			default:
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("broker: accept: %w", err)
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = conn.Close() // too many connections
			continue
		}
		s.mu.Lock()
		s.active++
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				<-s.slots
				s.mu.Lock()
				s.active--
				s.last = time.Now()
				s.mu.Unlock()
			}()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) audit(c Caller, method string, params []byte, result string) {
	r := AuditRecord{Time: time.Now(), UID: c.UID, PID: c.PID, Method: method, Result: result}
	r.TS = r.Time.UnixMilli()
	if len(params) > 0 {
		sum := sha256.Sum256(params)
		r.ParamsHash = hex.EncodeToString(sum[:])
	}
	s.cfg.Audit.Record(r)
	s.cfg.Log.Info("broker: call", "uid", c.UID, "pid", c.PID, "method", method, "result", result)
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	c, err := s.cfg.PeerCred(conn)
	if err != nil {
		s.cfg.Log.Warn("broker: peer credentials", "err", err)
		return
	}
	enc := json.NewEncoder(conn)
	if !s.cfg.InGroup(c.UID) {
		s.audit(c, "(connect)", nil, "refused: not in group "+Group)
		_ = enc.Encode(Response{Error: Errorf(CodeNotMember, "uid %d is not in group %s", c.UID, Group)})
		return
	}
	c.Start = startTime(s.cfg.ProcRoot, c.PID)
	r := bufio.NewReaderSize(conn, MaxLine)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.cfg.Idle))
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.audit(c, "(oversized)", nil, "refused: request over 64 KiB")
			_ = enc.Encode(Response{Error: Errorf(CodeInvalid, "request too large")})
			return
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded) {
				s.cfg.Log.Warn("broker: read", "err", err)
			}
			return
		}
		send := func(resp Response) error {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			return enc.Encode(resp)
		}
		if !s.call(ctx, c, bytes.TrimSpace(line), conn, r, send) {
			return
		}
	}
}

// call runs one request line and reports whether the connection takes
// another request (not after a failed write or a stream).
func (s *Server) call(ctx context.Context, c Caller, line []byte, conn net.Conn, r *bufio.Reader, send func(Response) error) bool {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		s.audit(c, "(malformed)", line, "refused: not a request")
		return send(Response{Error: Errorf(CodeParse, "not a JSON request")}) == nil
	}
	m, ok := s.methods[req.Method]
	if !ok {
		s.audit(c, req.Method, req.Params, "refused: unknown method")
		return send(Response{ID: req.ID, Error: Errorf(CodeNoMethod, "unknown method %q", req.Method)}) == nil
	}
	fail := func(err error) bool {
		var be *Error
		if !errors.As(err, &be) {
			be = Errorf(CodeFailed, "%v", err)
		}
		s.audit(c, req.Method, req.Params, "error: "+be.Message)
		return send(Response{ID: req.ID, Error: be}) == nil
	}
	timeout := s.cfg.CallTimeout
	if m.Timeout > 0 {
		timeout = m.Timeout
	}
	if m.Privileged {
		actx, cancel := context.WithTimeout(ctx, timeout)
		ok, err := s.cfg.Authority.Check(actx, c, m.Action)
		cancel()
		if err != nil || !ok {
			// Fail closed: a polkit error is a refusal.
			why := "refused: not authorized"
			if err != nil {
				why = "refused: authorization failed: " + err.Error()
			}
			s.audit(c, req.Method, req.Params, why)
			return send(Response{ID: req.ID, Error: Errorf(CodeUnauthorized, "not authorized")}) == nil
		}
	}
	if m.Stream != nil || m.Session != nil {
		s.stream(ctx, c, req, m, conn, r, send)
		return false
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := m.Handle(hctx, c, req.Params)
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		s.audit(c, req.Method, req.Params, "error: result")
		return send(Response{ID: req.ID, Error: Errorf(CodeInternal, "result could not be encoded")}) == nil
	}
	s.audit(c, req.Method, req.Params, "ok")
	return send(Response{ID: req.ID, Result: raw}) == nil
}

// stream runs a streaming method or a session: each item is a response
// with More set, then a last one without (an error, or no result). It
// ends when the method does or the caller closes the connection; the
// connection is closed after. During a stream the caller may only close
// the connection; during a session it sends lines for the method.
func (s *Server) stream(ctx context.Context, c Caller, req Request, m Method, conn net.Conn, r *bufio.Reader, send func(Response) error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_ = conn.SetReadDeadline(time.Time{})
	in := make(chan json.RawMessage, 16)
	go func() {
		defer cancel() // EOF (or anything, during a stream) ends it
		defer close(in)
		for {
			line, err := r.ReadSlice('\n')
			if err != nil || m.Session == nil {
				return // includes a line over MaxLine
			}
			select {
			case in <- json.RawMessage(bytes.Clone(bytes.TrimSpace(line))):
			case <-ctx.Done():
				return
			}
		}
	}()
	s.audit(c, req.Method, req.Params, "ok: stream")
	item := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return Errorf(CodeInternal, "item could not be encoded")
		}
		if err := send(Response{ID: req.ID, Result: raw, More: true}); err != nil {
			cancel()
			return err
		}
		return nil
	}
	var err error
	if m.Session != nil {
		err = m.Session(ctx, c, req.Params, in, item)
	} else {
		err = m.Stream(ctx, c, req.Params, item)
	}
	if ctx.Err() != nil {
		s.audit(c, req.Method, nil, "ended: closed")
		return // the caller left, or the broker is stopping
	}
	final := Response{ID: req.ID}
	if err != nil {
		var be *Error
		if !errors.As(err, &be) {
			be = Errorf(CodeFailed, "%v", err)
		}
		final.Error = be
		s.audit(c, req.Method, req.Params, "error: stream: "+be.Message)
	} else {
		s.audit(c, req.Method, nil, "ended")
	}
	_ = send(final)
}

// peerCred reads SO_PEERCRED of a Unix socket connection.
func peerCred(conn net.Conn) (Caller, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Caller{}, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Caller{}, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) // #nosec G115 -- a socket fd fits in int
	}); err != nil {
		return Caller{}, err
	}
	if cerr != nil {
		return Caller{}, cerr
	}
	return Caller{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}

// startTime reads field 22 of /proc/<pid>/stat (0 if unreadable); polkit
// uses it with the pid to identify the process.
func startTime(procRoot string, pid int32) uint64 {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(int(pid)), "stat")) // #nosec G304 -- fixed /proc layout, pid from the kernel
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return 0
	}
	v, _ := strconv.ParseUint(f[19], 10, 64)
	return v
}
