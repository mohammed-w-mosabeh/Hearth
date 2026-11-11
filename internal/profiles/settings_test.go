package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

var (
	dndOn     = Item{Kind: KindSetting, Target: "dnd", Do: "on"}
	bright70  = Item{Kind: KindSetting, Target: "brightness", Do: "70"}
	wallpaper = Item{Kind: KindSetting, Target: "wallpaper", Do: "~/w.jpg"}
	vpn       = Item{Kind: KindConnection, Target: "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11", Do: DoStart}
	homeWiFi  = Item{Kind: KindConnection, Target: "11111111-2222-3333-4444-555555555555", Do: DoStop}
)

func (f *fixture) settingsRig() {
	f.h.settings["dnd"], f.h.settings["brightness"], f.h.settings["wallpaper"] = "off", "40", "'file:///old.jpg'|'file:///old-d.jpg'"
	f.h.conns[homeWiFi.Target] = true
}

// A switch records what each setting was and what it left; revert puts
// back exactly that (ROADMAP 10.5).
func TestSettingsSnapshotAndRevert(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	id := f.add(t, "Work", false, dndOn, bright70, wallpaper, vpn, homeWiFi)
	res, _ := f.activate(t, id)
	if res.Done != 5 || f.h.settings["dnd"] != "on" || f.h.settings["brightness"] != "70~" || !f.h.conns[vpn.Target] || f.h.conns[homeWiFi.Target] {
		t.Fatalf("%+v %v %v", res, f.h.settings, f.h.conns)
	}
	undo := f.profile(t, id).Undo
	for _, want := range []string{`"do":"off","expect":"on"`, `"do":"40","expect":"70~"`, `"do":"'file:///old.jpg'|'file:///old-d.jpg'","expect":"~/w.jpg"`} {
		if !strings.Contains(undo, want) {
			t.Fatalf("%s lacks %s", undo, want)
		}
	}
	f.h.calls = nil
	res = f.deactivate(t, id)
	if res.Done != 5 || f.h.settings["dnd"] != "off" || f.h.settings["wallpaper"] != "'file:///old.jpg'|'file:///old-d.jpg'" ||
		f.h.conns[vpn.Target] || !f.h.conns[homeWiFi.Target] {
		t.Fatalf("%+v %v %v", res, f.h.settings, f.h.conns)
	}
	// The restore ran in reverse order: the VPN goes down before home Wi-Fi
	// comes back.
	if f.h.calls[0] != "network/disconnect "+vpn.Target || f.h.calls[len(f.h.calls)-1] != "network/connect "+homeWiFi.Target {
		t.Fatalf("%v", f.h.calls)
	}
	if !f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("still active")
	}
}

// A setting changed by hand after the switch is not overwritten.
func TestRevertLeavesHandChanges(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	id := f.add(t, "Work", false, dndOn, bright70)
	f.activate(t, id)
	f.h.settings["brightness"] = "55" // the user moved the slider
	f.h.calls = nil
	_, plan := f.preview(t, "deactivate", id)
	blocked, _ := plan["blocked"].([]any)
	if len(blocked) != 1 || !strings.Contains(mustJSON(blocked), `"reason":"changed"`) {
		t.Fatalf("%+v", plan)
	}
	res := f.deactivate(t, id)
	if res.Done != 1 || f.h.settings["brightness"] != "55" || f.h.settings["dnd"] != "off" {
		t.Fatalf("%+v %v", res, f.h.settings)
	}
	if slices.ContainsFunc(f.h.calls, func(c string) bool { return strings.HasPrefix(c, "set/brightness") }) {
		t.Fatalf("%v", f.h.calls)
	}
	// What it leaves alone is given up: the mode is no longer active.
	if !f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal("still active")
	}
}

// A setting already as wanted is not touched and not restored; one that
// failed is not restored either.
func TestSettingsPartial(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	f.h.settings["dnd"] = "on"
	f.h.failOn["set/brightness 70"] = true
	id := f.add(t, "Work", false, dndOn, bright70, wallpaper)
	res, _ := f.activate(t, id)
	if res.Done != 1 || len(res.Failed) != 1 {
		t.Fatalf("%+v", res)
	}
	undo := f.profile(t, id).Undo
	if strings.Contains(undo, "dnd") || strings.Contains(undo, "brightness") || !strings.Contains(undo, "wallpaper") {
		t.Fatalf("%s", undo)
	}
}

// What restores a mode survives a restart of hearthd.
func TestSettingsRestoreAfterRestart(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	id := f.add(t, "Work", false, dndOn)
	f.activate(t, id)
	// A new manager, as after a restart, reads the restore list stored.
	again := New(f.m.cfg)
	ctx := context.Background()
	p := f.profile(t, id)
	pr, blocked, err := again.planDeactivate(ctx, p)
	if err != nil || len(blocked) != 0 {
		t.Fatal(err, blocked)
	}
	if _, err := again.applyDeactivate(ctx, pr, true); err != nil {
		t.Fatal(err)
	}
	if f.h.settings["dnd"] != "off" || !f.profile(t, id).ActivatedAt.IsZero() {
		t.Fatal(f.h.settings)
	}
}

// A file cannot plant a restore value: expect is refused on save.
func TestExpectIsNotSaved(t *testing.T) {
	f := setup(t)
	rec := f.do(t, "POST", "/api/v1/profiles", `{"name":"x","items":[{"kind":"setting","target":"dnd","do":"on","expect":"off"}]}`)
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Session settings and connections may run while a game starts (S15b).
func TestGameMaySetSettings(t *testing.T) {
	f := setup(t)
	f.settingsRig()
	f.add(t, "Game", true, dndOn, vpn)
	f.h.games = []string{"steam.1"}
	f.tick(context.Background(), t)
	if f.h.settings["dnd"] != "on" || !f.h.conns[vpn.Target] {
		t.Fatalf("%v %v", f.h.settings, f.h.conns)
	}
}

// preview asks for an action's plan without running it.
func (f *fixture) preview(t *testing.T, action string, id int64) (int, map[string]any) {
	t.Helper()
	rec := f.do(t, "POST", "/api/v1/actions/profile/"+action, fmt.Sprintf(`{"target":"%d","preview":true}`, id))
	var plan map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	return rec.Code, plan
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
