package terminal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestDir(t *testing.T) {
	for _, ok := range []string{"", "/", "/home/me/My Projects", "/tmp/x-1"} {
		if _, valid := Dir(ok); !valid {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"relative", "/a/../b", "/a/", "/a//b", "/a\nb", "/a\x00b", "/a\x1b[31m", "/" + strings.Repeat("a", 5000)} {
		if _, valid := Dir(bad); valid {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShell(t *testing.T) {
	dir := t.TempDir()
	passwd, shells := filepath.Join(dir, "passwd"), filepath.Join(dir, "shells")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(shells, "# /etc/shells\n/bin/sh\n/bin/bash\n/usr/bin/zsh\n")
	write(passwd, "root:x:0:0:root:/root:/bin/bash\nme:x:1000:1000:Me,,,:/home/me:/usr/bin/zsh\nodd:x:1001:1001::/home/odd:/home/odd/evil\n")
	s := New(Config{UID: 1000, Passwd: passwd, Shells: shells})
	if got := s.Shell(); got != "/usr/bin/zsh" {
		t.Errorf("listed shell: %s", got)
	}
	s.cfg.UID = 1001 // a shell /etc/shells does not list
	if got := s.Shell(); got != "/bin/bash" {
		t.Errorf("unlisted shell: %s", got)
	}
	write(shells, "/bin/sh\n")
	if got := s.Shell(); got != "/bin/sh" {
		t.Errorf("fallback: %s", got)
	}
}

func TestProps(t *testing.T) {
	get := func(ps []property, name string) any {
		for _, p := range ps {
			if p.Name == name {
				return p.Value.Value()
			}
		}
		return nil
	}
	p := Props("/usr/bin/zsh", "", 7)
	if get(p, "WorkingDirectory") != "~" || get(p, "StandardInputFileDescriptor") != dbus.UnixFD(7) ||
		get(p, "StandardErrorFileDescriptor") != dbus.UnixFD(7) || get(p, "Slice") != "app.slice" {
		t.Errorf("%+v", p)
	}
	ex, _ := get(p, "ExecStartEx").([]execEx)
	if len(ex) != 1 || ex[0].Path != "/usr/bin/zsh" || len(ex[0].Argv) != 1 || ex[0].Argv[0] != "-zsh" || ex[0].Flags[0] != "no-env-expand" {
		t.Errorf("exec %+v", ex)
	}
	if get(Props("/bin/sh", "/srv/data", 3), "WorkingDirectory") != "-/srv/data" {
		t.Error("dir")
	}
}

// fakeBus records the units started and stopped; started is given the
// slave fd so the test can play the shell.
type fakeBus struct {
	mu      sync.Mutex
	started []string
	stopped []string
	fail    bool
	shell   chan *os.File
}

func (f *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, _ []any, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case sdManager + ".StartTransientUnit":
		if f.fail {
			return errors.New("org.freedesktop.systemd1.UnitExists")
		}
		f.started = append(f.started, args[0].(string))
		for _, p := range args[2].([]property) {
			if p.Name == "StandardInputFileDescriptor" {
				fd := int(p.Value.Value().(dbus.UnixFD))
				// Like systemd: keep a duplicate of the fd it was given.
				d, err := dupFd(fd)
				if err != nil {
					return err
				}
				f.shell <- os.NewFile(uintptr(d), "shell")
			}
		}
	case sdManager + ".StopUnit":
		f.stopped = append(f.stopped, args[0].(string))
	}
	return nil
}

func (f *fakeBus) Close() error { return nil }

// dial opens a terminal WebSocket; status is the HTTP status of a refused
// upgrade (0 when it was accepted).
func dial(t *testing.T, srv *httptest.Server, query string) (conn *websocket.Conn, status int, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query, &websocket.DialOptions{Subprotocols: []string{auth.Subprotocol}})
	if resp != nil {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			status = resp.StatusCode
		}
	}
	return conn, status, err
}

func TestShellSession(t *testing.T) {
	bus := &fakeBus{shell: make(chan *os.File, 4)}
	var audits []store.AuditEntry
	var amu sync.Mutex
	s := New(Config{UID: 1000, Max: 1, Bus: func(context.Context) (Bus, error) { return bus, nil },
		Audit: func(_ context.Context, e store.AuditEntry) error {
			amu.Lock()
			audits = append(audits, e)
			amu.Unlock()
			return nil
		}})
	srv := httptest.NewServer(http.HandlerFunc(s.HandleShell))
	defer srv.Close()

	conn, _, err := dial(t, srv, "cols=90&rows=20&dir=/srv")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, msg, err := conn.Read(ctx); err != nil || string(msg) != `{"state":"ready"}` {
		t.Fatalf("%s %v", msg, err)
	}
	shell := <-bus.shell
	// A second terminal is refused while the first is open (Max 1).
	if _, status, err := dial(t, srv, ""); err == nil || status != http.StatusTooManyRequests {
		t.Errorf("limit: %d %v", status, err)
	}
	// Shell output reaches the browser; browser input reaches the shell.
	if _, err := shell.Write([]byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	if typ, msg, err := conn.Read(ctx); err != nil || typ != websocket.MessageBinary || !strings.Contains(string(msg), "hello") {
		t.Fatalf("output %q %v", msg, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"resize":[120,40]}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 64)
	_ = shell.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := shell.Read(got)
	if err != nil || !strings.Contains(string(got[:n]), "ls") {
		t.Fatalf("input %q %v", got[:n], err)
	}
	// The shell exits: the browser is told, and the unit is stopped.
	_ = shell.Close()
	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatal("no exit state:", err)
		}
		if typ == websocket.MessageText {
			if string(msg) != `{"state":"exit"}` {
				t.Fatalf("%s", msg)
			}
			break
		}
	}
	// Answer the server's close, as a browser does.
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("close: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		bus.mu.Lock()
		done := len(bus.stopped) == 1 && bus.stopped[0] == bus.started[0]
		bus.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unit not stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasPrefix(bus.started[0], "hearth-term-") || !strings.HasSuffix(bus.started[0], ".service") {
		t.Errorf("unit %s", bus.started[0])
	}
	amu.Lock()
	if len(audits) != 1 || audits[0].Action != "terminal/open" || audits[0].Target != "/srv" || !strings.HasPrefix(audits[0].Result, "ok: hearth-term-") {
		t.Errorf("audit %+v", audits)
	}
	amu.Unlock()
}

func TestShellRefused(t *testing.T) {
	bus := &fakeBus{fail: true, shell: make(chan *os.File, 1)}
	s := New(Config{Bus: func(context.Context) (Bus, error) { return bus, nil }})
	srv := httptest.NewServer(http.HandlerFunc(s.HandleShell))
	defer srv.Close()
	for _, q := range []string{"dir=relative", "cols=0", "rows=5000", "dir=/a/../b"} {
		if _, status, err := dial(t, srv, q); err == nil || status != http.StatusBadRequest {
			t.Errorf("%q: %d", q, status)
		}
	}
	conn, _, err := dial(t, srv, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg, err := conn.Read(context.Background()); err != nil || !strings.Contains(string(msg), `"state":"exit","error":"the shell could not be started"`) {
		t.Errorf("%s %v", msg, err)
	}
}

// An idle shell writes nothing, so only closing the pty can end the read of
// its output: its unit and its slot must go when the browser does.
func TestIdleShellEndsWithBrowser(t *testing.T) {
	bus := &fakeBus{shell: make(chan *os.File, 1)}
	s := New(Config{Max: 1, Bus: func(context.Context) (Bus, error) { return bus, nil }})
	srv := httptest.NewServer(http.HandlerFunc(s.HandleShell))
	conn, _, err := dial(t, srv, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg, err := conn.Read(context.Background()); err != nil || string(msg) != `{"state":"ready"}` {
		t.Fatalf("%s %v", msg, err)
	}
	shell := <-bus.shell
	defer srv.Close()
	defer func() { _ = shell.Close() }() // first: it releases a stuck read
	time.Sleep(200 * time.Millisecond)   // the server is waiting for output
	_ = conn.CloseNow()

	deadline := time.Now().Add(3 * time.Second)
	for {
		bus.mu.Lock()
		stopped := len(bus.stopped) == 1
		bus.mu.Unlock()
		if stopped && s.open.Load() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle shell outlived its browser")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func FuzzDir(f *testing.F) {
	for _, s := range []string{"", "/", "/home/me", "rel", "/a/../b", "/a\nb", "/a/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, ok := Dir(s)
		if !ok {
			return
		}
		if d != s || (s != "" && (!filepath.IsAbs(s) || filepath.Clean(s) != s || strings.Contains(s, "/../") || strings.HasSuffix(s, "/..") || strings.ContainsAny(s, "\x00\n\r\x1b\x7f") || len(s) > 4096)) {
			t.Fatalf("accepted %q", s)
		}
	})
}
