package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestUninstallOps(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	op := UninstallOp{ID: "a1", Item: "app:vlc", Name: "VLC", Created: now.UnixMilli(), Updated: now.UnixMilli(), State: "running", Record: json.RawMessage(`{"steps":[]}`)}
	if err := db.SaveUninstallOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.State, op.Record = "done", json.RawMessage(`{"steps":[1]}`)
	if err := db.SaveUninstallOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	old := op
	old.ID, old.Created = "b2", now.Add(-8*24*time.Hour).UnixMilli()
	if err := db.SaveUninstallOp(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUninstallOp(ctx, UninstallOp{ID: "c", Record: json.RawMessage(`{`)}); err == nil {
		t.Fatal("stored invalid JSON")
	}
	got, err := db.UninstallOps(ctx, now.Add(-7*24*time.Hour))
	if err != nil || len(got) != 1 || got[0].State != "done" || string(got[0].Record) != `{"steps":[1]}` {
		t.Fatalf("%+v %v", got, err)
	}
	if err := db.PruneUninstallOps(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GetUninstallOp(ctx, "b2"); ok {
		t.Fatal("an old operation stayed")
	}
	if g, ok, err := db.GetUninstallOp(ctx, "a1"); !ok || err != nil || g.Name != "VLC" {
		t.Fatal(g, ok, err)
	}
}

func TestTakePlaytime(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if err := db.AddPlaytime(ctx, "desktop.x", 30, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	p, ok, err := db.TakePlaytime(ctx, "desktop.x")
	if err != nil || !ok || p.Seconds != 30 || p.LastPlayed != 100 {
		t.Fatal(p, ok, err)
	}
	if _, ok, _ := db.TakePlaytime(ctx, "desktop.x"); ok {
		t.Fatal("taken twice")
	}
	if err := db.AddPlaytime(ctx, "desktop.x", 5, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if err := db.PutPlaytime(ctx, p); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Playtimes(ctx)
	if all["desktop.x"] != 35 {
		t.Fatal(all)
	}
}
