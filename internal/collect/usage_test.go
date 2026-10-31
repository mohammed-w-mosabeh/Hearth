package collect

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type usageStore struct {
	rows   []store.ItemUsage
	pruned time.Time
}

func (s *usageStore) AddItemUsage(_ context.Context, rows []store.ItemUsage) error {
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *usageStore) PruneItemUsage(_ context.Context, before time.Time) error {
	s.pruned = before
	return nil
}

func writeCgroup(t *testing.T, root, rel string, usec, mem uint64, populated bool) {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := "0"
	if populated {
		p = "1"
	}
	for name, body := range map[string]string{
		"cpu.stat":       "usage_usec " + strconv.FormatUint(usec, 10) + "\nuser_usec 1\n",
		"memory.current": strconv.FormatUint(mem, 10) + "\n",
		"cgroup.events":  "populated " + p + "\nfrozen 0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsageRecorder(t *testing.T) {
	root := t.TempDir()
	const cid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	app := "/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-org.gnome.Nautilus-4242.scope"
	app2 := "/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-org.gnome.Nautilus-4343.scope"
	set := func(cups, docker, nautilus, nautilus2, idle uint64) {
		writeCgroup(t, root, "/system.slice/cups.service", cups, 10<<20, true)
		writeCgroup(t, root, "/system.slice/docker-"+cid+".scope", docker, 512<<20, true)
		writeCgroup(t, root, "/system.slice/system-getty.slice/getty@tty1.service", idle, 1<<20, true)
		writeCgroup(t, root, "/system.slice/stopped.service", 0, 0, false)
		writeCgroup(t, root, app, nautilus, 100<<20, true)
		writeCgroup(t, root, app2, nautilus2, 50<<20, true)
		writeCgroup(t, root, "/user.slice/user-1000.slice/session-2.scope", 1e12, 1<<30, true) // a login session: not an item
	}
	st := &usageStore{}
	name := func(id string) (string, bool) {
		n, ok := map[string]string{"app:org.gnome.Nautilus": "Files"}[id]
		return n, ok
	}
	u := NewUsageRecorder(UsageConfig{CgroupRoot: root, UID: 1000, Store: st, Name: name})
	u.ncpu = 4

	t0 := time.Date(2026, 10, 29, 10, 0, 30, 0, time.UTC)
	set(1e6, 1e6, 1e6, 1e6, 1e6)
	u.Sample(t0)
	// One minute later: cups used 2.4 s of CPU (1% of 4 cores), the
	// container 24 s (10%), Nautilus's two windows 12 s each (10% together),
	// getty 0.
	set(1e6+2.4e6, 1e6+24e6, 1e6+12e6, 1e6+12e6, 1e6)
	u.Sample(t0.Add(time.Minute))
	u.flush(context.Background())

	got := map[string]store.ItemUsage{}
	for _, r := range st.rows {
		got[r.Item] = r
	}
	near := func(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }
	if r := got["unit:cups.service"]; !near(r.CPUAvg, 1) || r.Kind != KindService || r.N != 1 || !r.Time.Equal(t0.Truncate(time.Hour)) {
		t.Errorf("cups %+v", r)
	}
	if r := got["container:0123456789ab"]; !near(r.CPUAvg, 10) || r.MemMax != 512<<20 || r.Kind != KindContainer {
		t.Errorf("container %+v", r)
	}
	if r := got["app:org.gnome.Nautilus"]; !near(r.CPUAvg, 10) || r.MemAvg != 150<<20 || r.Name != "Files" {
		t.Errorf("app (two scopes summed, named) %+v", r)
	}
	// getty: no CPU and 1 MiB, under both thresholds; the stopped unit and
	// the session are not items.
	if len(st.rows) != 3 {
		t.Errorf("rows %+v", st.rows)
	}
	if st.rows[0].Item != "container:0123456789ab" {
		t.Errorf("busiest first: %+v", st.rows)
	}
	if st.pruned.IsZero() {
		t.Error("not pruned")
	}

	// The hour starts over after a flush.
	st.rows = nil
	u.flush(context.Background())
	if len(st.rows) != 0 {
		t.Errorf("second flush %+v", st.rows)
	}
}

func TestUsageNow(t *testing.T) {
	root := t.TempDir()
	unit := filepath.Join(root, "system.slice/busy.service")
	if err := os.MkdirAll(unit, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(usec string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(unit, "cpu.stat"), []byte("usage_usec "+usec+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(unit, "cgroup.events"), []byte("populated 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("1000")
	u := NewUsageRecorder(UsageConfig{CgroupRoot: root, UID: 1000, Name: func(string) (string, bool) { return "Busy", true }})
	done := make(chan []ItemNow)
	go func() {
		got, _ := u.Now(context.Background(), 50*time.Millisecond, 5)
		done <- got
	}()
	time.Sleep(10 * time.Millisecond)
	write("100000000")
	got := <-done
	if len(got) != 1 || got[0].Name != "Busy" || got[0].CPU <= 0 {
		t.Fatalf("%+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.Now(ctx, time.Minute, 5); err == nil {
		t.Error("a cancelled request should end")
	}
}
