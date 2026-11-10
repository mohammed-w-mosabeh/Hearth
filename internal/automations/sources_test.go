package automations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type watchState struct {
	mu      sync.Mutex
	idle    float64
	games   []string
	apps    map[string]bool
	ssh     int
	sshAsk  int
	sec     int
	images  map[string]bool
	resumes chan struct{}
}

func (w *watchState) set(f func()) { w.mu.Lock(); f(); w.mu.Unlock() }

func sourcesRig(t *testing.T) (*engineRig, *watchState) {
	g := newEngineRig(t)
	ws := &watchState{apps: map[string]bool{}, images: map[string]bool{}, resumes: make(chan struct{}, 1)}
	g.e.cfg.Watch = Watch{
		Home:        t.TempDir(),
		IdleSeconds: func(context.Context) (float64, error) { ws.mu.Lock(); defer ws.mu.Unlock(); return ws.idle, nil },
		Games:       func() ([]string, error) { ws.mu.Lock(); defer ws.mu.Unlock(); return ws.games, nil },
		RunningApps: func() map[string]bool { ws.mu.Lock(); defer ws.mu.Unlock(); return ws.apps },
		SSHFailures: func(context.Context, time.Time) (int, []string, error) {
			ws.mu.Lock()
			defer ws.mu.Unlock()
			ws.sshAsk++
			return ws.ssh, []string{"192.0.2.9"}, nil
		},
		SecurityUpdates: func(context.Context) (int, error) { ws.mu.Lock(); defer ws.mu.Unlock(); return ws.sec, nil },
		ImageUpdates: func(context.Context) (map[string]bool, error) {
			ws.mu.Lock()
			defer ws.mu.Unlock()
			return ws.images, nil
		},
		Resumes: func(context.Context) (<-chan struct{}, error) { return ws.resumes, nil },
	}
	return g, ws
}

func (g *engineRig) tick() { g.e.Tick(context.Background()); g.e.Wait() }

func TestSources(t *testing.T) {
	g, ws := sourcesRig(t)
	g.add("idle", `{"trigger":{"kind":"idle","minutes":30},"then":[{"step":"action","action":"power/profile","target":"idle"}]}`)
	g.add("game", `{"trigger":{"kind":"app","state":"started","game":true},"then":[{"step":"action","action":"power/profile","target":"game"}]}`)
	g.add("app", `{"trigger":{"kind":"app","state":"stopped","app":"org.x.Editor"},"then":[{"step":"action","action":"power/profile","target":"app"}]}`)
	g.add("ssh", `{"trigger":{"kind":"ssh_failed","count":10},"then":[{"step":"action","action":"power/profile","target":"ssh"}]}`)
	g.add("sec", `{"trigger":{"kind":"security_updates"},"then":[{"step":"action","action":"power/profile","target":"sec"}]}`)
	g.add("img", `{"trigger":{"kind":"image_update","target":"web"},"then":[{"step":"action","action":"power/profile","target":"img"}]}`)
	ws.set(func() {
		ws.idle, ws.games, ws.apps["org.x.Editor"], ws.ssh, ws.sec = 2000, []string{"steam.1"}, true, 20, 3
	})
	g.tick() // what it first finds: nothing fires
	if g.ranList() != "" {
		t.Fatalf("fired on first sight: %q", g.ranList())
	}
	ws.set(func() { ws.idle, ws.games, ws.apps = 10, nil, map[string]bool{}; ws.ssh = 0 })
	g.now = g.now.Add(SSHEvery)
	g.tick()
	ws.set(func() {
		ws.idle, ws.games, ws.ssh, ws.sec, ws.images = 1900, []string{"steam.2"}, 12, 4, map[string]bool{"other": true, "web": true}
	})
	g.now = g.now.Add(SSHEvery)
	g.tick()
	for _, want := range []string{"idle", "game", "app", "ssh", "sec", "img"} {
		if strings.Count(g.ranList(), "power/profile "+want) != 1 {
			t.Errorf("%s: %q", want, g.ranList())
		}
	}
	// SSH is counted every 5 minutes at most.
	asked := ws.sshAsk
	g.tick()
	if ws.sshAsk != asked {
		t.Error("SSH counted again within 5 minutes")
	}
}

func TestFilesOlderThan(t *testing.T) {
	g, _ := sourcesRig(t)
	home := g.e.cfg.Watch.Home
	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(filepath.Join(dl, "sub", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := g.now.Add(-40 * 24 * time.Hour)
	for _, f := range []string{"a.pdf", "b.txt", "sub/c.pdf", "sub/deep/d.pdf"} {
		p := filepath.Join(dl, f)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	if err := os.WriteFile(filepath.Join(dl, "new.pdf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(dl, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	if n := countOld(dl, 1, "*.pdf", g.now.Add(-30*24*time.Hour)); n != 2 {
		t.Fatalf("counted %d (a.pdf and sub/c.pdf)", n)
	}
	g.add("dl", `{"trigger":{"kind":"files","folder":"~/Downloads","event":"older_than","days":30,"depth":0,"pattern":"*"},
		"then":[{"step":"action","action":"power/profile","target":"files"}]}`)
	g.tick()
	g.tick() // once a day
	if strings.Count(g.ranList(), "files") != 1 {
		t.Fatalf("%q", g.ranList())
	}
	// A folder outside the home through a link is refused.
	if err := os.Symlink("/etc", filepath.Join(home, "out")); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.e.folder("~/out"); ok {
		t.Fatal("a link out of the home was followed")
	}
}

func TestFilesNewAndResume(t *testing.T) {
	g, ws := sourcesRig(t)
	home := g.e.cfg.Watch.Home
	if err := os.MkdirAll(filepath.Join(home, "Inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.e.runCtx = ctx
	g.e.watchResume(ctx)
	g.add("new", `{"trigger":{"kind":"files","folder":"~/Inbox","event":"new","depth":0,"pattern":"*.pdf"},
		"then":[{"step":"action","action":"power/profile","target":"newfile"}]}`)
	g.add("resume", `{"trigger":{"kind":"resume"},"then":[{"step":"action","action":"power/profile","target":"resume"}]}`)
	if err := os.WriteFile(filepath.Join(home, "Inbox", "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Inbox", "scan.pdf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws.resumes <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (!strings.Contains(g.ranList(), "newfile") || !strings.Contains(g.ranList(), "resume")) {
		time.Sleep(20 * time.Millisecond)
	}
	g.e.Wait()
	if strings.Count(g.ranList(), "newfile") != 1 || strings.Count(g.ranList(), "resume") != 1 {
		t.Fatalf("%q", g.ranList())
	}
}

func TestContainerAndGameConditions(t *testing.T) {
	g, _ := sourcesRig(t)
	g.e.cfg.State.ContainerState = func(_ context.Context, name string) (string, error) { return "running unhealthy", nil }
	g.e.cfg.State.GameRunning = func() bool { return true }
	g.add("c", `{"trigger":{"kind":"usb_device"},"if":{"match":"all","conditions":[{"kind":"container","target":"web","state":"unhealthy"},{"kind":"game","is":true}]},
		"then":[{"step":"action","action":"power/profile","target":"cond"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	if g.ranList() != "power/profile cond" {
		t.Fatalf("%q", g.ranList())
	}
}
