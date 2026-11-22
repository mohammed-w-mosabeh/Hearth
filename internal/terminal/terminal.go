// Package terminal serves the Terminal page's shells (SPEC 13, SECURITY
// S14): a pseudo-terminal opened by hearthd whose slave end is handed,
// as a file descriptor, to a transient unit of the user's systemd manager
// running the user's login shell. The shell runs as the user, outside
// hearthd's sandbox (it would otherwise see a read-only home and no
// devices), and hearthd never executes anything itself.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// One WebSocket per shell: GET /api/v1/terminal?cols=&rows=&dir=. Binary
// frames carry terminal bytes both ways; text frames carry control: the
// browser sends {"resize":[cols,rows]}, hearthd sends
// {"state":"ready"|"exit","error":...} (as the container shell does).

const (
	sdDest    = "org.freedesktop.systemd1"
	sdPath    = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManager = "org.freedesktop.systemd1.Manager"
)

// Bus is the user's systemd manager connection (*dbusx.Conn).
type Bus interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Close() error
}

// Config configures the terminal.
type Config struct {
	Bus   func(context.Context) (Bus, error) // the session bus
	Audit func(ctx context.Context, e store.AuditEntry) error
	UID   int
	Log   *slog.Logger
	// Passwd and Shells default to /etc/passwd and /etc/shells.
	Passwd, Shells string
	// Max is the number of shells open at once (default 8).
	Max int
}

// Server serves terminals.
type Server struct {
	cfg  Config
	open atomic.Int32
}

// New returns a Server.
func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Passwd == "" {
		cfg.Passwd = "/etc/passwd"
	}
	if cfg.Shells == "" {
		cfg.Shells = "/etc/shells"
	}
	if cfg.Max == 0 {
		cfg.Max = 8
	}
	ptmxPath() // now, before any child could inherit the descriptor
	return &Server{cfg: cfg}
}

// maxFrame bounds one browser frame (a paste).
const maxFrame = 32 << 10

type state struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type input struct {
	Resize []int `json:"resize"`
}

// Dir validates a working directory: "" (home) or an absolute, clean path
// without control characters.
func Dir(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	if len(s) > 4096 || !filepath.IsAbs(s) || filepath.Clean(s) != s {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// Shell is the user's login shell from passwd when /etc/shells lists it,
// else /bin/bash, else /bin/sh.
func (s *Server) Shell() string {
	listed := map[string]bool{}
	if b, err := os.ReadFile(s.cfg.Shells); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "/") {
				listed[line] = true
			}
		}
	}
	if b, err := os.ReadFile(s.cfg.Passwd); err == nil {
		uid := strconv.Itoa(s.cfg.UID)
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) == 7 && f[2] == uid && listed[f[6]] && filepath.Clean(f[6]) == f[6] {
				return f[6]
			}
		}
	}
	if listed["/bin/bash"] || listed["/usr/bin/bash"] {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// D-Bus argument shapes of StartTransientUnit (ssa(sv)a(sa(sv))).
type (
	property struct {
		Name  string
		Value dbus.Variant
	}
	auxUnit struct {
		Name  string
		Props []property
	}
	execEx struct { // ExecStartEx a(sasas)
		Path  string
		Argv  []string
		Flags []string
	}
)

// Props are the transient unit's properties: the shell as a login shell
// (argv[0] "-bash"), its standard streams the terminal fd, which systemd
// makes the controlling terminal of the service's session. "-dir" lets a
// missing folder fall back to home.
func Props(shell, dir string, tty int) []property {
	wd := "~"
	if dir != "" {
		wd = "-" + dir
	}
	fd := dbus.UnixFD(int32(tty)) // #nosec G115 -- a file descriptor
	return []property{
		{"Description", dbus.MakeVariant("Hearth terminal")},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
		{"Slice", dbus.MakeVariant("app.slice")},
		{"WorkingDirectory", dbus.MakeVariant(wd)},
		{"Environment", dbus.MakeVariant([]string{"TERM=xterm-256color", "COLORTERM=truecolor"})},
		{"SendSIGHUP", dbus.MakeVariant(true)},
		{"StandardInputFileDescriptor", dbus.MakeVariant(fd)},
		{"StandardOutputFileDescriptor", dbus.MakeVariant(fd)},
		{"StandardErrorFileDescriptor", dbus.MakeVariant(fd)},
		{"ExecStartEx", dbus.MakeVariant([]execEx{{Path: shell,
			Argv: []string{"-" + filepath.Base(shell)}, Flags: []string{"no-env-expand"}}})},
	}
}

func unitName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "hearth-term-" + hex.EncodeToString(b) + ".service"
}

// HandleShell serves the terminal WebSocket.
func (s *Server) HandleShell(w http.ResponseWriter, r *http.Request) {
	cols, rows := size(r, "cols", 80), size(r, "rows", 24)
	dir, ok := Dir(r.URL.Query().Get("dir"))
	if !ok || cols == 0 || rows == 0 {
		http.Error(w, "bad folder or terminal size", http.StatusBadRequest)
		return
	}
	if s.open.Add(1) > int32(s.cfg.Max) { // #nosec G115 -- a small limit
		s.open.Add(-1)
		http.Error(w, "too many terminals are open", http.StatusTooManyRequests)
		return
	}
	defer s.open.Add(-1)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{auth.Subprotocol}})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	if conn.Subprotocol() != auth.Subprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol "+auth.Subprotocol+" required")
		return
	}
	conn.SetReadLimit(maxFrame)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	control := func(st state) {
		b, _ := json.Marshal(st)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	exit := func(err error) {
		st := state{State: "exit"}
		if err != nil {
			st.Error = err.Error()
		}
		control(st)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	target := dir
	if target == "" {
		target = "~"
	}
	audit := func(result string) {
		if s.cfg.Audit == nil {
			return
		}
		if err := s.cfg.Audit(context.WithoutCancel(ctx), store.AuditEntry{
			Origin: store.OriginLocal, UID: s.cfg.UID, Action: "terminal/open", Target: target, Result: result,
		}); err != nil {
			s.cfg.Log.Warn("terminal: audit", "err", err)
		}
	}

	master, unit, err := s.start(ctx, dir, cols, rows)
	if err != nil {
		s.cfg.Log.Warn("terminal: start", "err", err)
		audit("error: " + err.Error())
		exit(errors.New("the shell could not be started"))
		return
	}
	defer func() {
		_ = master.Close()
		s.stop(unit)
	}()
	audit("ok: " + unit)
	control(state{State: "ready"})

	// Browser → shell.
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := master.Write(data); err != nil {
					return
				}
				continue
			}
			var in input
			if json.Unmarshal(data, &in) == nil && len(in.Resize) == 2 && valid(in.Resize[0]) && valid(in.Resize[1]) {
				_ = setSize(master, in.Resize[0], in.Resize[1])
			}
		}
	}()
	// Closing the master ends the read below when the browser goes away.
	go func() {
		<-ctx.Done()
		_ = master.Close()
	}()
	// Shell → browser, until the shell exits (EIO once nothing holds the
	// slave end).
	buf := make([]byte, 32<<10)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				exit(nil)
			}
			return
		}
	}
}

// start opens a pty of cols x rows and starts the shell's unit on it.
func (s *Server) start(ctx context.Context, dir string, cols, rows int) (*os.File, string, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, "", err
	}
	// systemd keeps its own copy of the slave fd; hearthd's goes now.
	defer func() { _ = slave.Close() }()
	if err := setSize(master, cols, rows); err != nil {
		_ = master.Close()
		return nil, "", err
	}
	bus, err := s.cfg.Bus(ctx)
	if err != nil {
		_ = master.Close()
		return nil, "", err
	}
	defer func() { _ = bus.Close() }()
	unit := unitName()
	var job dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".StartTransientUnit", []any{&job},
		unit, "fail", Props(s.Shell(), dir, int(slave.Fd())), []auxUnit{}); err != nil { // #nosec G115 -- a file descriptor
		_ = master.Close()
		return nil, "", err
	}
	return master, unit, nil
}

// stop stops the shell's unit (its whole process tree) if it still runs.
func (s *Server) stop(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, err := s.cfg.Bus(ctx)
	if err != nil {
		return
	}
	defer func() { _ = bus.Close() }()
	var job dbus.ObjectPath
	_ = bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, unit, "replace") // already gone: NoSuchUnit
}

func valid(n int) bool { return n >= 1 && n <= 1000 }

func size(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || !valid(n) {
		return 0
	}
	return n
}
