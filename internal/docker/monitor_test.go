package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cgroup(t *testing.T, root, id string, usec, mem, inactive int) string {
	t.Helper()
	dir := filepath.Join(root, "system.slice", "docker-"+id+".scope")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cpu.stat", "usage_usec "+itoa(usec)+"\nuser_usec 1\n")
	write("memory.current", itoa(mem)+"\n")
	write("memory.stat", "anon 1\ninactive_file "+itoa(inactive)+"\n")
	return dir
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestMonitor(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "docker.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	web := strings.Repeat("a", 64)
	db := strings.Repeat("b", 64)
	cgroup(t, root, web, 1_000_000, 300<<20, 100<<20)
	cgroup(t, root, db, 0, 50<<20, 0)
	_ = os.MkdirAll(filepath.Join(root, "system.slice", "cron.service"), 0o750)

	fb := &fakeBroker{answers: map[string]string{
		"docker.containers.list": `[
			{"Id":"` + web + `","Names":["/web"],"State":"running","Status":"Up 2 hours"},
			{"Id":"` + db + `","Names":["/db"],"State":"running","Status":"Up 2 hours"},
			{"Id":"c1","Names":["/crashed"],"State":"exited","Status":"Exited (1) 3 minutes ago"},
			{"Id":"c2","Names":["/stopped"],"State":"exited","Status":"Exited (143) 1 hour ago"},
			{"Id":"c3","Names":["/done"],"State":"exited","Status":"Exited (0) 1 hour ago"},
			{"Id":"c4","Names":["/oom"],"State":"exited","Status":"Exited (137) 1 minute ago"},
			{"Id":"c5","Names":["/loop"],"State":"restarting","Status":"Restarting (1) 2 seconds ago"},
			{"Id":"c6","Names":["/new"],"State":"created","Status":"Created"}]`,
		"docker.containers.inspect": `{"inspect":{"State":{"OOMKilled":true}},"risks":[]}`,
	}}
	h := newHub()
	var seen []map[string]float64
	m := &Monitor{B: fb, Pub: h, CgroupRoot: root, Socket: sock,
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) { seen = append(seen, v) }}

	t0 := time.Unix(1000, 0)
	m.sample(context.Background(), t0)
	if len(fb.called()) != 2 { // list + inspect of the 137
		t.Fatalf("calls %v", fb.called())
	}
	if got := seen[0]; len(got) != 3 || got["crashed"] != 1 || got["oom"] != 1 || got["loop"] != 1 {
		t.Fatalf("unexpected %v", got)
	}

	// One second later: the list is not read again; CPU is a rate.
	cgroup(t, root, web, 1_500_000, 300<<20, 100<<20)
	m.sample(context.Background(), t0.Add(time.Second))
	if len(fb.called()) != 2 {
		t.Fatalf("list read again: %v", fb.called())
	}
	if m.last.Running[0].Name != "web" || m.last.Running[0].CPU != 50 || m.last.Running[0].Mem != 200<<20 || m.last.Stopped != 5 {
		b, _ := json.Marshal(m.last)
		t.Fatalf("%s", b)
	}

	// db stops: the running set changed, so the list is read again.
	_ = os.RemoveAll(filepath.Join(root, "system.slice", "docker-"+db+".scope"))
	m.sample(context.Background(), t0.Add(2*time.Second))
	if n := strings.Count(strings.Join(fb.called(), "\n"), "docker.containers.list"); n != 2 {
		t.Fatalf("lists %d", n)
	}
	if len(m.last.Running) != 1 {
		t.Fatalf("running %v", m.last.Running)
	}

	// Docker not installed: nothing asked, nothing observed.
	_ = os.Remove(sock)
	m.sample(context.Background(), t0.Add(3*time.Second))
	if m.last.Available || len(seen) != 3 {
		t.Fatalf("%v %d", m.last.Available, len(seen))
	}
}

func TestMonitorListFails(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "docker.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	fb := &fakeBroker{errs: map[string]error{"docker.containers.list": os.ErrNotExist}}
	m := &Monitor{B: fb, Pub: newHub(), CgroupRoot: root, Socket: sock,
		Observe: func(context.Context, time.Time, map[string]float64) { t.Fatal("observed without a list") }}
	t0 := time.Unix(1000, 0)
	for i := range 10 {
		cgroup(t, root, strings.Repeat(string(rune('a'+i)), 64), 0, 0, 0) // the set changes every time
		m.sample(context.Background(), t0.Add(time.Duration(i)*15*time.Second))
	}
	if n := len(fb.called()); n != 1 { // retried after Refresh only
		t.Fatalf("calls %d", n)
	}
}

func TestMonitorEventMetrics(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "docker.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	web := strings.Repeat("a", 64)
	dir := cgroup(t, root, web, 1, 1, 0)
	events := func(n int) {
		if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("low 0\nhigh 0\nmax 3\noom 1\noom_kill "+itoa(n)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	events(4) // kills from before Hearth looked are not news
	fb := &fakeBroker{answers: map[string]string{
		"docker.containers.list": `[
			{"Id":"` + web + `","Names":["/web"],"State":"running","Status":"Up 2 hours (unhealthy)"},
			{"Id":"c1","Names":["/fine"],"State":"running","Status":"Up 2 hours (healthy)"},
			{"Id":"c2","Names":["/stopping"],"State":"exited","Status":"Exited (0) (unhealthy)"}]`}}
	got := map[string]map[string]float64{}
	m := &Monitor{B: fb, Pub: newHub(), CgroupRoot: root, Socket: sock,
		Report: func(_ context.Context, _ time.Time, metric string, v map[string]float64) { got[metric] = v }}
	ctx, now := context.Background(), time.Now()
	m.sample(ctx, now)
	if len(got["container.unhealthy"]) != 1 || got["container.unhealthy"]["web"] != 1 {
		t.Fatalf("%v", got)
	}
	if len(got["container.oom"]) != 0 {
		t.Fatalf("baseline fired: %v", got)
	}
	events(5)
	m.sample(ctx, now.Add(15*time.Second))
	if got["container.oom"]["web"] != 1 {
		t.Fatalf("%v", got)
	}
	m.sample(ctx, now.Add(5*time.Minute)) // no new kill, still in the hold: one incident
	if got["container.oom"]["web"] != 1 {
		t.Fatalf("%v", got)
	}
	m.sample(ctx, now.Add(11*time.Minute))
	if len(got["container.oom"]) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestMonitorSeesARestart(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "docker.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	web := strings.Repeat("a", 64)
	dir := cgroup(t, root, web, 1_000_000, 300<<20, 0)
	fb := &fakeBroker{answers: map[string]string{
		"docker.containers.list": `[{"Id":"` + web + `","Names":["/web"],"State":"running","Status":"Up 2 hours"}]`,
	}}
	var got []string
	m := &Monitor{B: fb, Pub: newHub(), CgroupRoot: root, Socket: sock,
		Restarted: func(_ context.Context, _ time.Time, name string) { got = append(got, name) }}
	ctx := context.Background()
	t0 := time.Unix(1000, 0)
	m.sample(ctx, t0) // first seen: a start, not a restart
	m.sample(ctx, t0.Add(time.Second))
	if len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if err := os.Rename(dir, dir+".old"); err != nil { // a new directory, a new inode
		t.Fatal(err)
	}
	cgroup(t, root, web, 10, 300<<20, 0)
	m.sample(ctx, t0.Add(2*time.Second))
	if len(got) != 1 || got[0] != "web" {
		t.Fatalf("%v", got)
	}
}
