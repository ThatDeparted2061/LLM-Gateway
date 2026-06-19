package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestBucketDrainsAndRefills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := New(ctx, 10, 2) // 1 token per 100ms tick

	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("burst of 2 should be allowed")
	}
	if l.Allow("a") {
		t.Fatal("third request should be limited")
	}
	if !l.Allow("b") {
		t.Fatal("keys must have independent buckets")
	}
	time.Sleep(3 * refillEvery)
	if !l.Allow("a") {
		t.Fatal("bucket should have refilled")
	}
}
