package profiles

import (
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

// Stacks may ask for a password, so a game cannot switch them (SECURITY
// S15a); approvals come in 10.7.
func TestGameRefusesStacks(t *testing.T) {
	f := setup(t)
	rec := f.do(t, "POST", "/api/v1/profiles", `{"name":"G","auto_game":true,"items":[{"kind":"stack","target":"photos","do":"stop"}]}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
