package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTiles(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	a, err := db.CreateTile(ctx, Tile{Name: "A", URL: "http://a/", HealthCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := db.CreateTile(ctx, Tile{Name: "B", URL: "http://b/", Icon: "upload:s1"})
	c, _ := db.CreateTile(ctx, Tile{Name: "C", URL: "http://c/", Icon: "upload:s1"})
	_ = db.PutIcon(ctx, "s1", "image/png", []byte{1})
	_ = db.PutIcon(ctx, "s1", "image/png", []byte{1}) // twice is fine

	if err := db.OrderTiles(ctx, []int64{c, a}); err != nil {
		t.Fatal(err)
	}
	ts, _ := db.Tiles(ctx)
	if len(ts) != 3 || ts[0].ID != c || ts[1].ID != a || ts[2].ID != b || !ts[1].HealthCheck {
		t.Fatalf("order %+v", ts)
	}

	// The shared icon stays until its last tile lets go.
	if err := db.DeleteTile(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Icon(ctx, "s1"); err != nil {
		t.Fatalf("icon dropped while in use: %v", err)
	}
	if err := db.UpdateTile(ctx, Tile{ID: c, Name: "C2", URL: "http://c/", Icon: "builtin:globe"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Icon(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unused icon kept: %v", err)
	}
	if err := db.DeleteTile(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := db.UpdateTile(ctx, Tile{ID: 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update: %v", err)
	}

	_ = db.PutIcon(ctx, "orphan", "image/png", []byte{2})
	_ = db.DropOrphanIcons(ctx, time.Hour)
	if _, _, err := db.Icon(ctx, "orphan"); err != nil {
		t.Fatal("fresh upload dropped")
	}
	_ = db.DropOrphanIcons(ctx, -time.Second)
	if _, _, err := db.Icon(ctx, "orphan"); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan kept")
	}
}
