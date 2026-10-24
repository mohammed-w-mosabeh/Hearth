package store

import (
	"slices"
	"sync"
	"testing"
)

func TestRing(t *testing.T) {
	r := NewRing[int](3)
	if _, ok := r.Last(); ok || r.Len() != 0 || len(r.Snapshot()) != 0 {
		t.Fatal("new ring not empty")
	}
	for i := 1; i <= 2; i++ {
		r.Push(i)
	}
	if got := r.Snapshot(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("partial: %v", got)
	}
	for i := 3; i <= 7; i++ {
		r.Push(i)
	}
	if got := r.Snapshot(); !slices.Equal(got, []int{5, 6, 7}) || r.Len() != 3 {
		t.Fatalf("wrapped: %v len %d", got, r.Len())
	}
	if v, ok := r.Last(); !ok || v != 7 {
		t.Fatalf("last %d %v", v, ok)
	}

	// Snapshot is a copy.
	snap := r.Snapshot()
	snap[0] = 99
	if r.Snapshot()[0] == 99 {
		t.Fatal("snapshot aliases the ring")
	}
}

func TestRingExactlyFull(t *testing.T) {
	r := NewRing[int](2)
	r.Push(1)
	r.Push(2)
	if got := r.Snapshot(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("%v", got)
	}
	if v, _ := r.Last(); v != 2 {
		t.Fatalf("last %d", v)
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing[int](64)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				r.Push(g*1000 + i)
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
	if r.Len() != 64 {
		t.Fatalf("len %d", r.Len())
	}
}
