package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestModeArming(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	at := time.UnixMilli(1793359297763)
	for _, a := range []ModeArming{{"work", 0, "h1", at}, {"work", 1, "h1", at}, {"battery", 0, "b1", at}} {
		if err := db.ArmModeTrigger(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	// Arming again replaces, it does not duplicate.
	if err := db.ArmModeTrigger(ctx, ModeArming{"work", 1, "h2", at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	list, err := db.ModeArmings(ctx)
	if err != nil || len(list) != 3 || list[0].ModeID != "battery" || list[2].FileHash != "h2" || !list[0].ArmedAt.Equal(at) {
		t.Fatalf("%v %+v", err, list)
	}
	// The file is now h2: the trigger armed for h1 is disarmed.
	if n, err := db.DisarmModeStale(ctx, "work", "h2"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := db.DisarmModeStale(ctx, "battery", ""); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := db.DisarmModeTrigger(ctx, "work", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.DisarmModeTrigger(ctx, "work", 1); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.ModeArmings(ctx); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestActiveModes(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	at := time.UnixMilli(1793359297763)
	if err := db.SetActiveMode(ctx, ActiveMode{"work", "h1", at.Add(time.Second), "user", `{"undo":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetActiveMode(ctx, ActiveMode{"battery", "b1", at, "battery", `{}`}); err != nil {
		t.Fatal(err)
	}
	// Setting one again replaces its record.
	if err := db.SetActiveMode(ctx, ActiveMode{"work", "h2", at.Add(time.Second), "game", `{"undo":[1]}`}); err != nil {
		t.Fatal(err)
	}
	list, err := db.ActiveModes(ctx)
	if err != nil || len(list) != 2 || list[0].ModeID != "battery" || list[1].FileHash != "h2" || list[1].By != "game" || !list[0].Since.Equal(at) {
		t.Fatalf("%v %+v", err, list)
	}
	if err := db.ClearActiveMode(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.ActiveModes(ctx); len(list) != 1 || list[0].ModeID != "battery" {
		t.Fatalf("%+v", list)
	}
}

func TestModeNumbersAndAdopt(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	p1, _ := db.AddProfile(ctx, Profile{Name: "Dev", Items: `[]`})
	p2, _ := db.AddProfile(ctx, Profile{Name: "Games", Items: `[]`, AutoGame: true})
	// 0022 ran before these profiles existed; set the sequence as it would
	// be on a machine that had them.
	if _, err := db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = 'mode_ids'`, p2); err != nil {
		t.Fatal(err)
	}
	nums, err := db.ModeNumbers(ctx, []string{"quiet", "quiet"})
	if err != nil || len(nums) != 1 || nums["quiet"] <= p2 {
		t.Fatalf("%v %v", err, nums)
	}
	at := time.UnixMilli(1793359297763)
	if err := db.AdoptProfile(ctx, AdoptedProfile{ProfileID: p2, ModeID: "games",
		Active: &ActiveMode{FileHash: "g1", Since: at, By: "game", Snapshot: `{"undo":[]}`},
		Arm:    &ModeArming{Trigger: 0, FileHash: "g1", ArmedAt: at}}); err != nil {
		t.Fatal(err)
	}
	// A failed adoption changes nothing: the number is taken.
	if err := db.AdoptProfile(ctx, AdoptedProfile{ProfileID: p1, ModeID: "games"}); err == nil {
		t.Fatal("adopted twice")
	}
	if _, err := db.Profile(ctx, p1); err != nil {
		t.Fatal("a failed adoption removed the profile:", err)
	}
	if err := db.AdoptProfile(ctx, AdoptedProfile{ProfileID: 999, ModeID: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := db.Profile(ctx, p2); !errors.Is(err, ErrNotFound) {
		t.Fatal("the adopted profile is still there")
	}
	nums, _ = db.ModeNumbers(ctx, nil)
	act, _ := db.ActiveModes(ctx)
	arm, _ := db.ModeArmings(ctx)
	if nums["games"] != p2 || len(act) != 1 || act[0].ModeID != "games" || len(arm) != 1 || arm[0].ModeID != "games" {
		t.Fatalf("%v %+v %+v", nums, act, arm)
	}
}

func TestModeApprovals(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	at := time.UnixMilli(1793359297763)
	id, err := db.AddModeApproval(ctx, ModeApproval{ModeID: "battery", FileHash: "b1", Reason: "battery",
		Actions: `[{"stack":"photos","verb":"stop"}]`, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := db.AddModeApproval(ctx, ModeApproval{ModeID: "work", FileHash: "h1", Reason: "schedule", Actions: `[]`, CreatedAt: at.Add(time.Minute)})
	list, err := db.PendingModeApprovals(ctx)
	if err != nil || len(list) != 2 || list[0].ID != id || list[0].State != ApprovalPending || !list[0].DecidedAt.IsZero() {
		t.Fatalf("%v %+v", err, list)
	}
	if err := db.DecideModeApproval(ctx, id, ApprovalPending, at); err == nil {
		t.Fatal("decided to pending")
	}
	if err := db.DecideModeApproval(ctx, id, ApprovalApproved, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// One decision wins: a second click finds nothing pending.
	if err := db.DecideModeApproval(ctx, id, ApprovalDismissed, at.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	a, err := db.ModeApproval(ctx, id)
	if err != nil || a.State != ApprovalApproved || !a.DecidedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("%v %+v", err, a)
	}
	if list, _ := db.PendingModeApprovals(ctx); len(list) != 1 || list[0].ID != id2 {
		t.Fatalf("%+v", list)
	}
	if err := db.PruneModeApprovals(ctx, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ModeApproval(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := db.ModeApproval(ctx, id2); err != nil {
		t.Fatal("a pending approval was pruned:", err)
	}
}
