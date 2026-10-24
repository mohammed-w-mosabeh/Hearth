package collect

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type procFixture struct {
	t          *testing.T
	root, cgrp string
}

func newProcFixture(t *testing.T) *procFixture {
	f := &procFixture{t: t, root: t.TempDir(), cgrp: t.TempDir()}
	f.cpu(1000)
	return f
}

func (f *procFixture) write(root, rel, s string) {
	f.t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *procFixture) cpu(total uint64) {
	f.write(f.root, "stat", fmt.Sprintf("cpu  %d 0 0 0 0 0 0 0 0 0\ncpu0 1 0 0 0\n", total))
}

func (f *procFixture) proc(pid int, comm, cgroup string, ticks, flags, rssPages uint64) {
	// after ")": state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt
	// utime stime cutime cstime prio nice threads itreal starttime vsize rss
	f.write(f.root, fmt.Sprintf("%d/stat", pid), fmt.Sprintf("%d (%s) S 1 1 1 0 -1 %d 0 0 0 0 %d 0 0 0 20 0 1 0 %d 1000 %d 0\n",
		pid, comm, flags, ticks, 100+pid, rssPages))
	f.write(f.root, fmt.Sprintf("%d/cgroup", pid), "0::"+cgroup+"\n")
	f.write(f.root, fmt.Sprintf("%d/cmdline", pid), "/usr/bin/"+comm+"\x00--flag\x00")
	f.write(f.root, fmt.Sprintf("%d/status", pid), "Name:\t"+comm+"\nUid:\t1000\t1000\t1000\t1000\n")
}

// pio writes /proc/<pid>/io.
func (f *procFixture) pio(pid int, rd, wr uint64) {
	f.write(f.root, fmt.Sprintf("%d/io", pid), fmt.Sprintf("rchar: 1\nwchar: 1\nread_bytes: %d\nwrite_bytes: %d\ncancelled_write_bytes: 0\n", rd, wr))
}

// cgMem writes a cgroup without the io controller (user apps).
func (f *procFixture) cgMem(path string, mem uint64) {
	f.write(f.cgrp, path+"/memory.current", fmt.Sprintf("%d\n", mem))
}

func (f *procFixture) cg(path string, mem, rd, wr uint64) {
	f.write(f.cgrp, path+"/memory.current", fmt.Sprintf("%d\n", mem))
	f.write(f.cgrp, path+"/io.stat", fmt.Sprintf("8:0 rbytes=%d wbytes=%d rios=1 wios=1 dbytes=0 dios=0\n259:0 rbytes=%d wbytes=0\n", rd, wr, rd))
}

func TestProcScanGroups(t *testing.T) {
	f := newProcFixture(t)
	const u = "/user.slice/user-1000.slice/user@1000.service/app.slice/"
	const app1, app2 = u + "app-gnome-com.microsoft.VSCode-4242.scope", u + "app-com.microsoft.VSCode-7.scope"
	f.proc(100, "code", app1, 0, 0, 300)
	f.proc(101, "code", app2, 0, 0, 200)
	f.proc(200, "cupsd", "/system.slice/cups.service", 0, 0, 50)
	f.proc(300, "bash", "/user.slice/user-1000.slice/session-2.scope", 0, 0, 10)
	f.proc(2, "kthreadd", "/", 0, 0x00200000, 0)
	const app3 = u + "app-gnome-org.gnome.TextEditor-9.scope"
	f.proc(400, "gnome-text-editor", app3, 0, 0, 20)
	f.proc(401, "gnome-text-editor", app3, 0, 0, 20)
	f.cgMem(app3, 300)
	f.pio(400, 0, 0)
	f.pio(401, 0, 0)
	f.cg(app1, 1000, 0, 0)
	f.cg(app2, 500, 0, 0)
	f.cg("/system.slice/cups.service", 70, 0, 0)

	ps := NewProcSampler(ProcConfig{ProcRoot: f.root, CgroupRoot: f.cgrp, Log: discard(),
		Users: func() map[int]string { return map[int]string{1000: "me"} }})
	ps.uid = 1000 // the fixture's processes are "ours"
	first, err := ps.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Partial || len(first.Groups) != 5 || first.Groups[0].ReadB != nil {
		t.Fatalf("first scan: %+v", first)
	}

	f.cpu(1100) // 100 jiffies elapsed machine-wide
	f.proc(100, "code", app1, 30, 0, 300)
	f.proc(101, "code", app2, 10, 0, 200)
	f.proc(200, "cupsd", "/system.slice/cups.service", 5, 0, 50)
	f.cg(app1, 1000, 4096, 1024)
	f.pio(400, 2048, 0)
	f.pio(401, 2048, 512)
	time.Sleep(20 * time.Millisecond)
	snap, err := ps.Scan()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Group{}
	for _, g := range snap.Groups {
		byID[g.ID] = g
	}
	page := pageSize()
	// Two launches of the same app are one group; memory comes from both
	// cgroups, not from RSS.
	code := byID["app:com.microsoft.VSCode"]
	if code.Kind != KindApp || code.Name != "VSCode" || code.Count != 2 || !near(code.CPU, 40) ||
		code.Mem != 1500 || code.User != "me" || code.ReadB == nil || *code.ReadB <= 0 || *code.WriteB <= 0 {
		t.Fatalf("code %+v", code)
	}
	if code.Procs[0].PID != 100 || !near(code.Procs[0].CPU, 30) || code.Procs[0].RSS != 300*page {
		t.Fatalf("busiest first: %+v", code.Procs)
	}
	// No io controller in the user's cgroups: own processes' /proc/<pid>/io.
	ed := byID["app:org.gnome.TextEditor"]
	if ed.Mem != 300 || ed.ReadB == nil || *ed.ReadB <= 0 || *ed.WriteB <= 0 || ed.IOPartial {
		t.Fatalf("editor %+v", ed)
	}
	cups := byID["unit:cups.service"]
	if cups.Kind != KindService || cups.Name != "cups" || !near(cups.CPU, 5) || cups.Mem != 70 || cups.ReadB == nil || *cups.ReadB != 0 {
		t.Fatalf("cups %+v", cups)
	}
	// No own cgroup: RSS sum, no I/O.
	if b := byID["exe:bash"]; b.Kind != KindSession || b.Mem != 10*page || b.ReadB != nil {
		t.Fatalf("bash %+v", b)
	}
	if k := byID["kernel"]; k.Kind != KindKernel || k.Count != 1 {
		t.Fatalf("kernel %+v", k)
	}
	if snap.Groups[0].ID != "app:com.microsoft.VSCode" {
		t.Fatalf("not sorted by CPU: %s", snap.Groups[0].ID)
	}

	// A reused pid (new start time) is a new process: no bogus delta.
	f.cpu(1200)
	f.write(f.root, "200/stat", "200 (cupsd) S 1 1 1 0 -1 0 0 0 0 0 999 0 0 0 20 0 1 0 77777 1000 50 0\n")
	snap, _ = ps.Scan()
	for _, g := range snap.Groups {
		if g.ID == "unit:cups.service" && g.CPU != 0 {
			t.Fatalf("pid reuse produced CPU %v", g.CPU)
		}
	}
}

func TestParseIOStat(t *testing.T) {
	got := parseIOStat([]byte("8:0 rbytes=10 wbytes=20 rios=1\n8:16 rbytes=5 wbytes=x\n"))
	if got.read != 15 || got.write != 20 {
		t.Fatalf("%+v", got)
	}
}

func TestGroupFor(t *testing.T) {
	const u = "/user.slice/user-1000.slice/user@1000.service/"
	cases := []struct{ cg, exe, id, kind, name string }{
		{u + "app.slice/app-org.chromium.Chromium-1234.scope", "chrome", "app:org.chromium.Chromium", KindApp, "Chromium"},
		{u + "app.slice/app-gnome-org.gnome.TextEditor-99.scope", "gnome-text-editor", "app:org.gnome.TextEditor", KindApp, "TextEditor"},
		{u + "app.slice/app-flatpak-org.mozilla.firefox@12.service", "firefox", "app:org.mozilla.firefox", KindApp, "firefox"},
		{u + "app.slice/app-org.gnome.Terminal.service", "gnome-terminal", "app:org.gnome.Terminal", KindApp, "Terminal"},
		{u + "app.slice/hearth-app-steam-3.scope", "steam", "app:steam", KindApp, "steam"},
		{u + "app.slice/app-my\\x2dtool-5.scope", "x", "app:my-tool", KindApp, "my-tool"},
		{u + "app.slice/gnome-keyring-daemon.service", "gnome-keyring-d", "user-unit:gnome-keyring-daemon.service", KindUserService, "gnome-keyring-daemon"},
		{"/system.slice/system-syncnotes.slice/syncnotes@me.service", "syncnotes", "unit:syncnotes@me.service", KindService, "syncnotes@me"},
		{"/system.slice/docker-8809c2af4d64facf2a9c.scope", "nginx", "container:8809c2af4d64", KindContainer, "Container 8809c2af4d64"},
		{"/user.slice/user-1000.slice/session-2.scope", "bash", "exe:bash", KindSession, "bash"},
		{"/init.scope", "systemd", "unit:init.scope", KindService, "systemd"},
		{"", "orphan", "exe:orphan", KindSession, "orphan"},
	}
	for _, c := range cases {
		g := groupFor(c.cg, c.exe)
		if g.id != c.id || g.kind != c.kind || g.name != c.name {
			t.Errorf("%s: got %+v", c.cg, g)
		}
	}
}

func TestParseProcStatOddComm(t *testing.T) {
	st, err := parseProcStat([]byte("42 (a) b (c)) R 7 1 1 0 -1 4194560 0 0 0 0 11 22 0 0 20 0 3 0 555 9000 77 0\n"))
	if err != nil || st.comm != "a) b (c)" || st.state != "R" || st.ppid != 7 || st.ticks != 33 || st.start != 555 || st.rss != 77 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, bad := range []string{"", "42 no parens", "42 (x) R 1"} {
		if _, err := parseProcStat([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProcScanRealSystem(t *testing.T) {
	ps := NewProcSampler(ProcConfig{Log: discard()})
	if _, err := ps.Scan(); err != nil {
		t.Skipf("no /proc: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	snap, err := ps.Scan()
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, g := range snap.Groups {
		n += g.Count
		if i < 12 {
			t.Logf("%-40s %-12s %5.1f%% %8.1f MiB n=%d io=%v", g.ID, g.Kind, g.CPU, float64(g.Mem)/(1<<20), g.Count, g.ReadB != nil)
		}
	}
	t.Logf("%d processes in %d groups, scan took %v", n, len(snap.Groups), took)
}

func BenchmarkProcScan(b *testing.B) {
	ps := NewProcSampler(ProcConfig{Log: discard()})
	if _, err := ps.Scan(); err != nil {
		b.Skip(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ps.Scan(); err != nil {
			b.Fatal(err)
		}
	}
}
