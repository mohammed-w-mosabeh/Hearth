package collect

import "testing"

func BenchmarkCollectRealSystem(b *testing.B) {
	s := New(Config{Log: discard()})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Collect(); err != nil {
			b.Fatal(err)
		}
	}
}
