package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProfiles(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	id, err := db.AddProfile(ctx, Profile{Name: "gaming", Items: `[{"kind":"app"}]`, AutoGame: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddProfile(ctx, Profile{Name: "Dev", Items: `[]`}); err != nil {
		t.Fatal(err)
	}
	list, err := db.Profiles(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Dev" || !list[1].AutoGame || !list[1].ActivatedAt.IsZero() {
		t.Fatalf("%v %+v", err, list)
	}
	at := time.UnixMilli(1793359297763)
	if err := db.SetProfileActive(ctx, id, at, "game", `[{"kind":"unit"}]`); err != nil {
		t.Fatal(err)
	}
	// Editing a profile keeps what restores it.
	if err := db.UpdateProfile(ctx, Profile{ID: id, Name: "Games", Items: `[]`}); err != nil {
		t.Fatal(err)
	}
	p, err := db.Profile(ctx, id)
	if err != nil || p.Name != "Games" || p.AutoGame || !p.ActivatedAt.Equal(at) || p.ActivatedBy != "game" || p.Undo != `[{"kind":"unit"}]` {
		t.Fatalf("%v %+v", err, p)
	}
	if err := db.ClearProfileActive(ctx, id); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.Profile(ctx, id); !p.ActivatedAt.IsZero() || p.ActivatedBy != "" || p.Undo != "[]" {
		t.Fatalf("%+v", p)
	}
	if err := db.UpdateProfile(ctx, Profile{ID: 99, Name: "x", Items: "[]"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := db.DeleteProfile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProfile(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
	if _, err := db.Profile(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("read: %v", err)
	}
}
