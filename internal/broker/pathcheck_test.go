package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
)

// pathServer answers each connection with the next canned reply.
func pathServer(t *testing.T, replies ...string) (PathChecker, chan string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "pc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, len(replies))
	go func() {
		for _, r := range replies {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			got <- line
			_, _ = c.Write([]byte(r + "\n"))
			_ = c.Close()
		}
	}()
	return PathChecker{Path: sock}, got
}

func TestPathChecker(t *testing.T) {
	ok := `{"ok":true,"refused":[],"roots":{"/srv":"/srv"},"binds":[{"service":"w","source":"/srv/a","target":"/a","real":"/srv/a","dev":"8:1","fs_root":"/srv/a"}]}`
	moved := strings.Replace(ok, `"fs_root":"/srv/a"`, `"fs_root":"/etc"`, 1)
	pc, got := pathServer(t, ok, ok, moved, `{"ok":false,"refused":["x: no"]}`, `{"ok":true,"refused":["x"]}`, `not json`)
	ctx := context.Background()
	spec := json.RawMessage(`{"version":1}`)

	first, err := pc.Check(ctx, spec)
	if err != nil || !first.OK || len(first.Binds) != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	if r, err := pc.Recheck(ctx, spec, first); err != nil || !r.OK {
		t.Fatalf("same answer: %+v %v", r, err)
	}
	if r, err := pc.Recheck(ctx, spec, first); err != nil || r.OK || !strings.Contains(r.Refused[0], "differently") {
		t.Fatalf("moved: %+v %v", r, err)
	}
	if r, err := pc.Check(ctx, spec); err != nil || r.OK {
		t.Fatalf("refused: %+v %v", r, err)
	}
	if _, err := pc.Check(ctx, spec); !errors.Is(err, ErrPathCheck) {
		t.Fatalf("inconsistent: %v", err)
	}
	if _, err := pc.Check(ctx, spec); !errors.Is(err, ErrPathCheck) {
		t.Fatalf("not json: %v", err)
	}
	if sent := <-got; !strings.HasPrefix(sent, `{"spec":{"version":1}}`) {
		t.Errorf("sent %q", sent)
	}
	if _, err := (PathChecker{Path: filepath.Join(t.TempDir(), "none")}).Check(ctx, spec); !errors.Is(err, ErrPathCheck) {
		t.Fatalf("no socket: %v", err)
	}
}

func TestVerifyLive(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o700); err != nil {
		t.Fatal(err)
	}
	mi := "1 0 0:5 / / rw - overlay overlay rw\n2 1 8:1 /srv/a /a rw - ext4 /dev/sda1 rw\n"
	if err := os.WriteFile(filepath.Join(proc, "42", "mountinfo"), []byte(mi), 0o600); err != nil {
		t.Fatal(err)
	}
	r := pathcheck.Reply{OK: true, Binds: []pathcheck.Resolved{{Real: "/srv/a", Dev: "8:1", FSRoot: "/srv/a"}, {Real: "/srv/b", Dev: "8:1", FSRoot: "/srv/b"}}}
	r.Binds[0].Service, r.Binds[0].Target = "w", "/a"
	r.Binds[1].Service, r.Binds[1].Target = "db", "/b"
	if bad, err := VerifyLive(proc, 42, "w", r); err != nil || len(bad) != 0 {
		t.Fatalf("w: %v %v", bad, err)
	}
	if bad, err := VerifyLive(proc, 42, "db", r); err != nil || len(bad) != 1 {
		t.Fatalf("db: %v %v", bad, err)
	}
	if bad, err := VerifyLive(proc, 42, "other", r); err != nil || bad != nil {
		t.Fatalf("no binds: %v %v", bad, err)
	}
	if _, err := VerifyLive(proc, 0, "w", r); err == nil {
		t.Fatal("pid 0")
	}
	if _, err := VerifyLive(proc, 43, "w", r); err == nil {
		t.Fatal("no process")
	}
}
