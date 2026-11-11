package profiles

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Gate 10: a hostile mode file cannot reach any broker method beyond its
// named stacks, and an automatic switch never reaches one that may prompt.
// Files are written straight into the folder, as an attacker with the
// user's files would; the fake engine records every action and every
// stack.batch.
func TestGate10HostileModeFiles(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "modes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var set *modes.Set
	var db *store.DB
	old := newStore
	t.Cleanup(func() { newStore = old })
	newStore = func(_ *testing.T, d *store.DB) testStore {
		db = d
		set = modes.NewSet(modes.Config{Dir: dir, Store: d})
		set.Load(ctx)
		return modeStore{ProfileStore: &modes.ProfileStore{Set: set, DB: d}, db: d}
	}
	f := setup(t)
	f.settingsRig()

	refused := map[string]string{
		"command.toml":   "name = \"C\"\ncommand = \"rm -rf ~\"\n[session]\ndo_not_disturb = true\n",
		"exec.toml":      "name = \"E\"\n[[item]]\nkind = \"exec\"\ntarget = \"/bin/sh\"\ndo = \"start\"\n",
		"image.toml":     "name = \"I\"\n[[stack]]\nname = \"web\"\ndo = \"start\"\nimage = \"evil:latest\"\n",
		"spec.toml":      "name = \"S\"\n[[stack]]\nname = \"web\"\ndo = \"start\"\n[stack.spec]\nprivileged = true\n",
		"verb.toml":      "name = \"V\"\n[[stack]]\nname = \"web\"\ndo = \"down-volumes\"\n",
		"traversal.toml": "name = \"T\"\n[[stack]]\nname = \"../../etc\"\ndo = \"stop\"\n",
		"flag.toml":      "name = \"F\"\n[[stack]]\nname = \"--privileged\"\ndo = \"start\"\n",
		"wall.toml":      "name = \"W\"\n[session]\nwallpaper = \"/etc/shadow.png\"\n",
		"trigger.toml":   "name = \"G\"\n[session]\ndo_not_disturb = true\n[[trigger]]\nkind = \"game\"\nrun = \"x\"\n",
		"big.toml":       "name = \"B\"\n[session]\ndo_not_disturb = true\n#" + strings.Repeat("x", 70<<10) + "\n",
	}
	for name, src := range refused {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Valid files that name stacks, one of them unknown: only those names
	// and only start or stop may reach the broker.
	manual := "name = \"Manual\"\n[session]\ndo_not_disturb = true\n" +
		"[[stack]]\nname = \"web\"\ndo = \"stop\"\n[[stack]]\nname = \"ghost\"\ndo = \"start\"\n" +
		"[[item]]\nkind = \"unit\"\ntarget = \"system/sshd.service\"\ndo = \"stop\"\n"
	game := "name = \"Game\"\n[session]\ndo_not_disturb = true\n" +
		"[[stack]]\nname = \"web\"\ndo = \"stop\"\n[[stack]]\nname = \"photos\"\ndo = \"stop\"\n" +
		"[[item]]\nkind = \"unit\"\ntarget = \"user/syncnotes.service\"\ndo = \"stop\"\n[[trigger]]\nkind = \"game\"\n"
	for name, src := range map[string]string{"manual.toml": manual, "game.toml": game} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set.Load(ctx)
	var ids []string
	for _, m := range set.Modes() {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"game", "manual"}) {
		t.Fatalf("loaded %v; errors %+v", ids, set.Errors())
	}
	if len(set.Errors()) != len(refused) {
		t.Fatalf("%+v", set.Errors())
	}

	f.h.on["stack:web"], f.h.on["stack:photos"], f.h.on["unit:system/sshd.service"], f.h.on["unit:user/syncnotes.service"] = true, true, true, true
	number := func(name string) int64 {
		list, err := f.db.Profiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.Name == name {
				return p.ID
			}
		}
		t.Fatalf("no profile %s", name)
		return 0
	}
	// Named stacks, start or stop, nothing else, in any batch so far.
	checkBatches := func(named ...string) {
		t.Helper()
		for _, b := range f.h.batches {
			for _, it := range b.items {
				if it.Kind != KindStack || !slices.Contains(named, it.Target) || (it.Do != DoStart && it.Do != DoStop) || it.Expect != "" {
					t.Fatalf("reached the broker: %+v", it)
				}
			}
		}
		for _, c := range f.h.calls {
			if strings.HasPrefix(c, "stack/") {
				t.Fatalf("a stack outside stack.batch: %s", c)
			}
		}
	}

	// A manual switch: its stacks go in one batch that may prompt once.
	id := number("Manual")
	f.activate(t, id)
	checkBatches("web", "ghost")
	if len(f.h.batches) != 1 || !f.h.batches[0].interactive {
		t.Fatalf("%+v", f.h.batches)
	}
	f.deactivate(t, id)
	checkBatches("web", "ghost")

	// The game trigger, armed by a click for this file: no batch can
	// prompt, so without grants nothing reaches the broker; the stacks
	// wait for approval and the rest runs.
	g, _ := set.Mode("game")
	if err := db.ArmModeTrigger(ctx, store.ModeArming{ModeID: "game", Trigger: 0, FileHash: g.Hash, ArmedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.h.batches, f.h.calls = nil, nil
	f.h.games = []string{"steam.1"}
	f.tick(ctx, t)
	if len(f.h.batches) != 0 {
		t.Fatalf("an automatic switch reached the broker: %+v", f.h.batches)
	}
	if len(f.h.deferred) != 1 || len(f.h.deferred[0].items) != 2 || f.h.on["unit:user/syncnotes.service"] {
		t.Fatalf("%+v %v", f.h.deferred, f.h.on)
	}
	for _, it := range f.h.deferred[0].items {
		if it.Kind != KindStack || (it.Target != "web" && it.Target != "photos") {
			t.Fatalf("deferred %+v", it)
		}
	}
	f.h.games = nil
	f.tick(ctx, t)
	f.tick(ctx, t)
	checkBatches("web", "photos")

	// With a grant for web's stop, the automatic batch holds web alone and
	// cannot prompt; photos still waits.
	f.h.grants = map[string][]string{"web": {DoStop}}
	f.h.on["stack:web"], f.h.on["unit:user/syncnotes.service"] = true, true
	f.h.batches, f.h.deferred = nil, nil
	f.h.games = []string{"steam.2"}
	f.tick(ctx, t)
	checkBatches("web")
	if len(f.h.batches) != 1 || f.h.batches[0].interactive || len(f.h.batches[0].items) != 1 {
		t.Fatalf("%+v", f.h.batches)
	}
	if len(f.h.deferred) != 1 || len(f.h.deferred[0].items) != 1 || f.h.deferred[0].items[0].Target != "photos" {
		t.Fatalf("%+v", f.h.deferred)
	}
	f.h.games = nil
	f.tick(ctx, t)
	f.tick(ctx, t)
	checkBatches("web", "photos")
	for _, b := range f.h.batches {
		if b.interactive {
			t.Fatalf("an automatic switch or its restore could prompt: %+v", f.h.batches)
		}
	}
}
