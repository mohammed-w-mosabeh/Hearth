package procctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
)

func TestTarget(t *testing.T) {
	for s, ok := range map[string]bool{
		"group:app:com.microsoft.VSCode": true, "group:unit:cups.service": true, "group:kernel": true,
		"group:exe:bash": true, "group:container:8809c2af4d64": true, "pid:1234:5678": true,
		"pid:0:1": false, "pid:12:": false, "pid:-1:2": false, "group:": false, "proc:1": false,
		"group:app:x\n": false, "group:app:" + string(make([]byte, 400)): false,
	} {
		if _, _, _, err := target(s); (err == nil) != ok {
			t.Errorf("%q: err=%v", s, err)
		}
	}
}

func TestCheck(t *testing.T) {
	c := &ctl{self: 4242}
	ref := func(pid int, name, group, kind string, own bool) collect.ProcRef {
		return collect.ProcRef{PID: pid, Start: 1, Name: name, User: "me", Own: own, Group: group, Kind: kind}
	}
	refs := []collect.ProcRef{
		ref(1, "systemd", "unit:init.scope", collect.KindService, false),
		ref(900, "systemd", "user-unit:init.scope", collect.KindUserService, true),
		ref(2, "kthreadd", "kernel", collect.KindKernel, false),
		ref(4242, "hearthd", "user-unit:hearthd.service", collect.KindUserService, true),
		ref(500, "cupsd", "unit:cups.service", collect.KindService, false),
		ref(600, "gnome-shell", "user-unit:org.gnome.Shell@wayland.service", collect.KindUserService, true),
		ref(700, "chromium", "app:org.chromium.Chromium", collect.KindApp, true),
		ref(800, "code", "app:com.microsoft.VSCode", collect.KindApp, true),
		// Same group as gnome-shell: blocked with it.
		ref(610, "mutter-x11-frames", "user-unit:org.gnome.Shell@wayland.service", collect.KindUserService, true),
		// Anything in session.slice is part of the session.
		{PID: 620, Start: 1, Name: "gnome-session-ctl", Own: true, Group: "user-unit:gnome-session-manager@gnome.service",
			Kind: collect.KindUserService, Cgroup: "/user.slice/user-1000.slice/user@1000.service/session.slice/gnome-session-manager@gnome.service"},
		{PID: 630, Start: 1, Name: "helper", Own: true, Group: "user-unit:gnome-session-manager@gnome.service",
			Kind: collect.KindUserService, Cgroup: "/user.slice/user-1000.slice/user@1000.service/session.slice/gnome-session-manager@gnome.service"},
	}
	reasons := func(action, browser string) map[int]string {
		ok, blocked := c.check(action, refs, browser)
		out := map[int]string{}
		for _, it := range ok {
			var pid int
			_, _ = fmt.Sscanf(it.ID, "pid:%d:", &pid)
			out[pid] = "ok"
		}
		for _, b := range blocked {
			var pid int
			_, _ = fmt.Sscanf(b.ID, "pid:%d:", &pid)
			out[pid] = b.Reason
		}
		return out
	}
	kill := reasons("kill", "")
	want := map[int]string{1: ReasonSystem, 900: ReasonSystem, 2: ReasonSystem, 4242: ReasonSelf, 500: ReasonOtherUser,
		600: ReasonSession, 700: "ok", 800: "ok", 610: ReasonSession, 620: ReasonSession, 630: ReasonSession}
	for pid, r := range want {
		if kill[pid] != r {
			t.Errorf("kill pid %d: %s, want %s", pid, kill[pid], r)
		}
	}
	freeze := reasons("freeze", "app:org.chromium.Chromium")
	if freeze[700] != ReasonBrowser || freeze[800] != "ok" || freeze[600] != ReasonSession {
		t.Errorf("freeze %v", freeze)
	}
	// Lowering priority of the shell is harmless; resuming anything is.
	if r := reasons("renice", ""); r[600] != "ok" {
		t.Errorf("renice shell: %s", r[600])
	}
	if r := reasons("resume", ""); r[600] != "ok" || r[500] != ReasonOtherUser {
		t.Errorf("resume %v", r)
	}
}

func selfStart(t *testing.T, c *ctl) uint64 {
	t.Helper()
	st, err := c.sampler().StartTime(os.Getpid())
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	return st
}

func TestSignalChecksStartTime(t *testing.T) {
	c := &ctl{cfg: Config{ProcRoot: "/proc", CgroupRoot: "/sys/fs/cgroup"}, self: -1}
	start := selfStart(t, c)
	// SIGCONT to a running process is a no-op: safe to send to ourselves.
	if err := c.signal(os.Getpid(), start, syscall.SIGCONT); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	if err := c.signal(os.Getpid(), start+1, syscall.SIGCONT); !errors.Is(err, errGone) {
		t.Fatalf("stale start time: %v", err)
	}
	prio, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The raw syscall returns 20 - nice.
	if err := c.renice(os.Getpid(), start, 20-prio); err != nil {
		t.Fatalf("renice to the current value: %v", err)
	}
	res := c.each(actions.Plan{Items: []actions.Item{{ID: fmt.Sprintf("pid:%d:%d", os.Getpid(), start+1)}}},
		func(pid int, st uint64) error { return c.signal(pid, st, syscall.SIGCONT) })
	if res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonGone {
		t.Fatalf("each: %+v", res)
	}
}

func TestBrowserGroup(t *testing.T) {
	root := t.TempDir()
	w := func(rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The browser's socket 127.0.0.1:50000 -> 127.0.0.1:7380, inode 777.
	w("net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 0100007F:1CD4 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 778 1\n"+
		"   1: 0100007F:C350 0100007F:1CD4 01 00000000:00000000 00:00000000 00000000  1000        0 777 1\n")
	const cg = "/user.slice/user-1000.slice/user@1000.service/app.slice/app-org.chromium.Chromium-12.scope"
	w("300/stat", "300 (chromium) S 1 1 1 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 999 1000 10 0\n")
	w("300/cgroup", "0::"+cg+"\n")
	w("300/cmdline", "/usr/lib/chromium/chromium\x00")
	w("300/status", fmt.Sprintf("Name:\tchromium\nUid:\t%d\t0\t0\t0\n", os.Getuid()))
	if err := os.MkdirAll(filepath.Join(root, "300/fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[777]", filepath.Join(root, "300/fd/41")); err != nil {
		t.Fatal(err)
	}
	c := &ctl{cfg: Config{ProcRoot: root, CgroupRoot: t.TempDir()}, self: -1}
	if g := c.browserGroup("127.0.0.1:50000"); g != "app:org.chromium.Chromium" {
		t.Fatalf("browser group %q", g)
	}
	for _, other := range []string{"127.0.0.1:50001", "10.0.0.2:50000", "garbage"} {
		if g := c.browserGroup(other); g != "" {
			t.Errorf("%s: %q", other, g)
		}
	}
}

func TestCloseAppPlan(t *testing.T) {
	root := t.TempDir()
	w := func(rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const slice = "/user.slice/user-1000.slice/user@1000.service/app.slice/"
	proc := func(pid int, name, cg string) {
		w(fmt.Sprintf("%d/stat", pid), fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 999 1000 10 0\n", pid, name))
		w(fmt.Sprintf("%d/cgroup", pid), "0::"+cg+"\n")
		w(fmt.Sprintf("%d/status", pid), fmt.Sprintf("Name:\t%s\nUid:\t%d\t0\t0\t0\n", name, os.Getuid()))
	}
	proc(300, "editor", slice+"app-hearth-editor@1a.service")
	proc(301, "editor", slice+"app-gnome-editor-5.scope")
	proc(302, "other", slice+"app-gnome-other-6.scope")
	c := &ctl{cfg: Config{ProcRoot: root, CgroupRoot: t.TempDir(), AppGroups: func(id string) ([]string, string, bool) {
		switch id {
		case "editor":
			return []string{"app:editor"}, "Editor", true
		case "idle":
			return nil, "Idle", true
		}
		return nil, "", false
	}}, self: -1}
	a := c.closeApp()
	p, err := a.Plan(context.Background(), actions.Request{Target: "editor"})
	if err != nil || len(p.Items) != 2 || p.Items[0].ID != "pid:300:999" || p.Items[1].ID != "pid:301:999" || a.Risk != actions.Destructive {
		t.Fatalf("%+v %v", p, err)
	}
	for _, id := range []string{"idle", "unknown"} {
		if _, err := a.Plan(context.Background(), actions.Request{Target: id}); err == nil {
			t.Errorf("%s planned", id)
		}
	}
	for _, bad := range []actions.Request{{Target: "../x"}, {Target: "editor", Params: map[string]string{"nice": "1"}}} {
		if a.Validate(bad) == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
