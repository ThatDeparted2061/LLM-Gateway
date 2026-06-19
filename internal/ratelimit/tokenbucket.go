// Package ratelimit implements a token bucket per API key.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

const (
	refillEvery = 100 * time.Millisecond
	idleTTL     = 10 * time.Minute // full buckets idle this long are dropped to bound memory
)

// Limiter holds one bucket per API key. Each request costs one token; a
// background goroutine refills every bucket at rate tokens/sec, capped at burst.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// New starts the refill goroutine, which runs until ctx is done.
func New(ctx context.Context, rate float64, burst int) *Limiter {
	l := &Limiter{buckets: map[string]*bucket{}, rate: rate, burst: float64(burst)}
	go l.refillLoop(ctx)
	return l
}

// Allow takes a token from key's bucket; false means the key is over its limit.
// New keys start with a full bucket.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst}
		l.buckets[key] = b
	}
	b.lastSeen = time.Now()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ponytail: one goroutine walks every bucket under a global lock each tick,
// O(keys) per 100ms. Fine for thousands of keys; beyond that refill lazily in
// Allow (tokens += elapsed*rate) and drop the goroutine.
func (l *Limiter) refillLoop(ctx context.Context) {
	t := time.NewTicker(refillEvery)
	defer t.Stop()
	add := l.rate * refillEvery.Seconds()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.mu.Lock()
			for key, b := range l.buckets {
				b.tokens = min(l.burst, b.tokens+add)
				if b.tokens == l.burst && now.Sub(b.lastSeen) > idleTTL {
					delete(l.buckets, key)
				}
			}
			l.mu.Unlock()
		}
	}
}
