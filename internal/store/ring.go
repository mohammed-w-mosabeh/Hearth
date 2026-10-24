package store

import "sync"

// Ring is a fixed-capacity, concurrency-safe buffer that keeps the most
// recent values; the oldest is overwritten when full.
type Ring[T any] struct {
	mu   sync.Mutex
	buf  []T
	next int
	full bool
}

// NewRing returns a Ring holding at most capacity values.
func NewRing[T any](capacity int) *Ring[T] {
	if capacity <= 0 {
		panic("store: ring capacity must be positive")
	}
	return &Ring[T]{buf: make([]T, capacity)}
}

// Push appends v, evicting the oldest value if the ring is full.
func (r *Ring[T]) Push(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Len returns the number of stored values.
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.buf)
	}
	return r.next
}

// Snapshot returns the stored values, oldest first, as a new slice.
func (r *Ring[T]) Snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]T(nil), r.buf[:r.next]...)
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}

// Last returns the most recent value, if any.
func (r *Ring[T]) Last() (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero T
	if !r.full && r.next == 0 {
		return zero, false
	}
	return r.buf[(r.next-1+len(r.buf))%len(r.buf)], true
}
