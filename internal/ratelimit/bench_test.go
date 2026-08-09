package ratelimit

import (
	"context"
	"strconv"
	"testing"
)

// BenchmarkAllowParallel hammers 1,000 keys from all CPUs through the single lock.
func BenchmarkAllowParallel(b *testing.B) {
	l := New(context.Background(), 1e9, 1e9)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			l.Allow(strconv.Itoa(i % 1000))
			i++
		}
	})
}
