package profiles

import (
	"context"
	"strings"
	"testing"
)

var (
	photos = Item{Kind: KindStack, Target: "photos", Do: DoStop}
	feeds  = Item{Kind: KindStack, Target: "feeds", Do: DoStop}
	notes  = Item{Kind: KindStack, Target: "notes", Do: DoStart}
)

// A manual switch runs all its stack steps in one batch, which may prompt
// once; restoring is one batch too (ROADMAP 10.4).
func TestStacksRunInOneBatch(t *testing.T) {
	f := setup(t)
	f.h.on["stack:photos"], f.h.on["stack:feeds"] = true, true
	f.h.on["unit:user/syncnotes.service"] = true
	id := f.add(t, "Battery", false, photos, userUnit, feeds, notes)
	res, plan := f.activate(t, id)
	if tok, _ := plan["token"].(string); tok == "" {
		t.Fatal("stopping stacks needs the preview's token")
	}
	if res.Done != 4 || len(f.h.batches) != 1 {
		t.Fatalf("%+v %+v", res, f.h.batches)
	}
	b := f.h.batches[0]
	if !b.interactive || len(b.items) != 3 || b.items[0] != photos || b.items[1] != feeds || b.items[2] != notes {
		t.Fatalf("%+v", b)
	}
	// Stack steps never went through stack/start or stack/stop one by one.
	for _, c := range f.h.calls {
		if strings.HasPrefix(c, "stack/") {
			t.Fatalf("%v", f.h.calls)
		}
	}
	f.deactivate(t, id)
	if len(f.h.batches) != 2 {
		t.Fatalf("%+v", f.h.batches)
	}
	back := f.h.batches[1].items
	if !f.h.batches[1].interactive || len(back) != 3 || back[0].Target != "notes" || back[0].Do != DoStop || back[2].Target != "photos" || back[2].Do != DoStart {
		t.Fatalf("%+v", back)
	}
	if !f.h.on["stack:photos"] || f.h.on["stack:notes"] {
		t.Fatalf("%v", f.h.on)
	}
}

// A stack already as wanted is left out; one that fails is not restored.
func TestStackBatchPartial(t *testing.T) {
	f := setup(t)
	f.h.on["stack:photos"], f.h.on["stack:feeds"] = true, false
	f.h.failOn["stack/stop photos"] = true
	f.h.on["stack:notes"] = false
	id := f.add(t, "Battery", false, photos, feeds, notes)
	res, _ := f.activate(t, id)
	if len(f.h.batches) != 1 || len(f.h.batches[0].items) != 2 || res.Done != 1 || len(res.Failed) != 1 {
		t.Fatalf("%+v %+v", res, f.h.batches)
	}
	p := f.profile(t, id)
	if strings.Contains(p.Undo, "photos") || !strings.Contains(p.Undo, "notes") {
		t.Fatalf("%s", p.Undo)
	}
}

// Without approvals, stacks may ask for a password, so a game cannot have
// them (SECURITY S15a).
func TestGameRefusesStacksWithoutApprovals(t *testing.T) {
	f := setup(t)
	f.m.cfg.Defer = nil
	rec := f.do(t, "POST", "/api/v1/profiles", `{"name":"G","auto_game":true,"items":[{"kind":"stack","target":"photos","do":"stop"}]}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// An automatic switch never runs a stack: its steps wait for approval and
// the rest runs (ROADMAP 10.7). Approving runs them in one batch that may
// prompt once, and revert undoes them first.
func TestAutomaticSwitchDefersStacks(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	f.h.on["stack:photos"], f.h.on["stack:feeds"] = true, false
	feedsStart := Item{Kind: KindStack, Target: "feeds", Do: DoStart}
	id := f.add(t, "Game", true, photos, feedsStart, dndOn)
	f.h.games = []string{"steam.1"}
	f.m.poll(context.Background())
	if len(f.h.batches) != 0 {
		t.Fatalf("an automatic switch ran stacks: %+v", f.h.batches)
	}
	if f.h.settings["dnd"] != "on" || !f.h.on["stack:photos"] {
		t.Fatalf("%v %v", f.h.settings, f.h.on)
	}
	if len(f.h.deferred) != 1 || f.h.deferred[0].id != id || f.h.deferred[0].by != ByGame || len(f.h.deferred[0].items) != 2 {
		t.Fatalf("%+v", f.h.deferred)
	}
	if p := f.profile(t, id); p.ActivatedBy != ByGame || strings.Contains(p.Undo, "photos") {
		t.Fatalf("%+v", p)
	}
	res, err := f.m.ApplyDeferred(context.Background(), id, f.h.deferred[0].items)
	if err != nil || res.Done != 2 || len(f.h.batches) != 1 || !f.h.batches[0].interactive {
		t.Fatalf("%v %+v %+v", err, res, f.h.batches)
	}
	undo := f.profile(t, id).Undo
	if !strings.HasPrefix(undo, `[{"kind":"stack","target":"feeds","do":"stop"},{"kind":"stack","target":"photos","do":"start"}`) || !strings.Contains(undo, "dnd") {
		t.Fatalf("%s", undo)
	}
	// The game ends: the restore runs by itself, so its batch may not prompt.
	f.h.games = nil
	f.m.poll(context.Background())
	f.m.poll(context.Background())
	if len(f.h.batches) != 2 || f.h.batches[1].interactive {
		t.Fatalf("%+v", f.h.batches)
	}
}

// Only a stack switched on and only stack steps can be approved.
func TestApplyDeferredRefusals(t *testing.T) {
	f := setup(t)
	id := f.add(t, "Battery", false, photos)
	if _, err := f.m.ApplyDeferred(context.Background(), id, []Item{photos}); err == nil || !strings.Contains(err.Error(), "no longer on") {
		t.Fatal(err)
	}
	f.h.on["stack:photos"] = true
	f.activate(t, id)
	for _, bad := range [][]Item{{userUnit}, {dndOn}, {{Kind: KindStack, Target: "x", Do: DoStop, Expect: "y"}}} {
		if _, err := f.m.ApplyDeferred(context.Background(), id, bad); err == nil {
			t.Errorf("approved %+v", bad)
		}
	}
	if _, err := f.m.ApplyDeferred(context.Background(), 99, []Item{photos}); err == nil {
		t.Fatal("approved for no profile")
	}
}

// A trigger's switch defers stacks as the game's does, and cannot pass
// itself off as the user, whose switch may prompt.
func TestAutoActivateForATrigger(t *testing.T) {
	f := setup(t)
	f.h.on["stack:photos"] = true
	id := f.add(t, "Battery", false, photos)
	ctx := context.Background()
	if err := f.m.AutoActivate(ctx, id, ByUser, "x"); err == nil {
		t.Fatal("an automatic switch claimed to be the user")
	}
	if err := f.m.AutoActivate(ctx, id, "battery", "the battery fell below 30%"); err != nil {
		t.Fatal(err)
	}
	if len(f.h.batches) != 0 || len(f.h.deferred) != 1 || f.h.deferred[0].by != "battery" {
		t.Fatalf("%+v %+v", f.h.batches, f.h.deferred)
	}
	if p := f.profile(t, id); p.ActivatedBy != "battery" || p.ActivatedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
	if len(f.h.notes) != 1 || !strings.Contains(f.h.notes[0], "the battery fell below 30%") {
		t.Fatalf("%v", f.h.notes)
	}
	// Already on: nothing again.
	if err := f.m.AutoActivate(ctx, id, "battery", "again"); err != nil || len(f.h.deferred) != 1 {
		t.Fatal(err, f.h.deferred)
	}
}

// A stack the owner granted runs in the automatic switch, in a batch that
// cannot prompt; the rest still wait (ROADMAP 10.8).
func TestGrantedStacksRunUnattended(t *testing.T) {
	f := setup(t)
	f.h.on["stack:photos"], f.h.on["stack:feeds"], f.h.on["stack:notes"] = true, true, false
	f.h.grants = map[string][]string{"photos": {"stop"}, "notes": {"stop"}} // notes may not be started
	id := f.add(t, "Game", true, photos, feeds, notes)
	f.h.games = []string{"steam.1"}
	f.m.poll(context.Background())
	if len(f.h.batches) != 1 || f.h.batches[0].interactive || len(f.h.batches[0].items) != 1 || f.h.batches[0].items[0] != photos {
		t.Fatalf("%+v", f.h.batches)
	}
	if len(f.h.deferred) != 1 || len(f.h.deferred[0].items) != 2 || f.h.deferred[0].items[0] != feeds || f.h.deferred[0].items[1] != notes {
		t.Fatalf("%+v", f.h.deferred)
	}
	if !strings.Contains(f.profile(t, id).Undo, `"target":"photos","do":"start"`) {
		t.Fatal(f.profile(t, id).Undo)
	}
}
