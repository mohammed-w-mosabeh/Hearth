package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSnapshotsAddListDelete(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	h := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if _, err := db.LatestAutoSnapshot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty: %v", err)
	}
	auto, err := db.AddSnapshot(ctx, Snapshot{Time: h, Auto: true, Data: []byte("auto")}, []Change{
		{Time: h, Source: "units", Kind: "added", Subject: "a", After: "enabled"},
		{Time: h, Source: "containers", Kind: "deleted", Subject: "b", Before: "nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	named, err := db.AddSnapshot(ctx, Snapshot{Time: h.Add(time.Hour), Label: "mine", Data: []byte("named!")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := db.Snapshots(ctx)
	if err != nil || len(list) != 2 || list[0].ID != named || list[0].Size != 6 || list[0].Data != nil || list[1].ID != auto || !list[1].Auto {
		t.Fatalf("%v %+v", err, list)
	}
	s, err := db.Snapshot(ctx, named)
	if err != nil || string(s.Data) != "named!" || s.Label != "mine" || !s.Time.Equal(h.Add(time.Hour)) {
		t.Fatalf("%v %+v", err, s)
	}
	// The newest automatic one, though a named one is newer.
	if s, err := db.LatestAutoSnapshot(ctx); err != nil || s.ID != auto {
		t.Fatalf("%v %+v", err, s)
	}
	if err := db.DeleteSnapshot(ctx, named); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSnapshot(ctx, named); !errors.Is(err, ErrNotFound) {
		t.Fatalf("again: %v", err)
	}
	if _, err := db.Snapshot(ctx, named); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read: %v", err)
	}
}

func TestChangesBetweenFilters(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	h := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	var cs []Change
	for i, src := range []string{"units", "containers", "listeners", "units"} {
		cs = append(cs, Change{Time: h.Add(time.Duration(i) * time.Minute), Source: src, Kind: "added", Subject: src})
	}
	if _, err := db.AddSnapshot(ctx, Snapshot{Time: h, Data: []byte("x")}, cs); err != nil {
		t.Fatal(err)
	}
	all := func(sources []string, from, to time.Time, limit int) []Change {
		t.Helper()
		out, err := db.ChangesBetween(ctx, from, to, sources, limit)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	end := h.Add(time.Hour)
	if got := all(nil, h, end, 10); len(got) != 4 || !got[0].Time.Equal(h.Add(3*time.Minute)) {
		t.Errorf("%+v", got)
	}
	if got := all([]string{"units"}, h, end, 10); len(got) != 2 || got[0].Source != "units" || got[1].Source != "units" {
		t.Errorf("%+v", got)
	}
	if got := all([]string{"containers", "listeners"}, h, end, 10); len(got) != 2 {
		t.Errorf("%+v", got)
	}
	if got := all([]string{"unit"}, h, end, 10); len(got) != 0 {
		t.Errorf("a part of a name matched: %+v", got)
	}
	if got := all(nil, h, h.Add(time.Minute), 10); len(got) != 1 {
		t.Errorf("to is exclusive: %+v", got)
	}
	if got := all(nil, h, end, 3); len(got) != 3 {
		t.Errorf("limit: %+v", got)
	}
}

func TestPruneSnapshots(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	h := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	old, err := db.AddSnapshot(ctx, Snapshot{Time: h.Add(-3 * time.Hour), Auto: true, Data: []byte("x")},
		[]Change{{Time: h.Add(-3 * time.Hour), Source: "units", Kind: "added", Subject: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := db.AddSnapshot(ctx, Snapshot{Time: h.Add(-2 * time.Hour), Auto: true, Data: []byte("x")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	named, err := db.AddSnapshot(ctx, Snapshot{Time: h.Add(-4 * time.Hour), Label: "mine", Data: []byte("x")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSnapshots(ctx, h); err != nil {
		t.Fatal(err)
	}
	list, err := db.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	left := map[int64]bool{}
	for _, s := range list {
		left[s.ID] = true
	}
	// The newest automatic one stays as the base of the next diff, and the
	// user's stays until deleted.
	if left[old] || !left[base] || !left[named] {
		t.Errorf("left %+v", list)
	}
	if cs, err := db.ChangesBetween(ctx, time.UnixMilli(0), h, nil, 10); err != nil || len(cs) != 0 {
		t.Errorf("changes %v %+v", err, cs)
	}
}
