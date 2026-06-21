// Package cache holds the gateway's response caches: exact-match and semantic.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

// Exact caches responses under SHA256(model + messages JSON) for a fixed TTL.
// ponytail: no size cap, entries just expire after the TTL. Add an LRU bound if
// prompt cardinality makes memory a problem.
type Exact struct {
	m   sync.Map // key -> exactEntry
	ttl time.Duration
}

type exactEntry struct {
	resp    *providers.ChatResponse
	expires time.Time
}

// NewExact starts a janitor goroutine that drops expired entries until ctx is done.
func NewExact(ctx context.Context, ttl time.Duration) *Exact {
	c := &Exact{ttl: ttl}
	go c.janitor(ctx, min(ttl, time.Minute))
	return c
}

// Key identifies a request by model and conversation. Sampling parameters are
// deliberately excluded: the cache serves one canonical answer per prompt.
func Key(model string, msgs []providers.Message) string {
	b, _ := json.Marshal(msgs) // plain strings, cannot fail
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Exact) Get(key string) (*providers.ChatResponse, bool) {
	v, ok := c.m.Load(key)
	if !ok {
		return nil, false
	}
	e := v.(exactEntry)
	if time.Now().After(e.expires) {
		c.m.Delete(key)
		return nil, false
	}
	return e.resp, true
}

func (c *Exact) Set(key string, resp *providers.ChatResponse) {
	c.m.Store(key, exactEntry{resp: resp, expires: time.Now().Add(c.ttl)})
}

func (c *Exact) janitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.m.Range(func(k, v any) bool {
				if now.After(v.(exactEntry).expires) {
					c.m.Delete(k)
				}
				return true
			})
		}
	}
}
