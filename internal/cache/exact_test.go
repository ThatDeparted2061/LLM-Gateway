package cache

import (
	"context"
	"testing"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

func TestExactHitAndExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewExact(ctx, 50*time.Millisecond)

	msgs := []providers.Message{{Role: "user", Content: "hi"}}
	key := Key("m", msgs)
	if Key("other-model", msgs) == key {
		t.Fatal("model must be part of the key")
	}

	c.Set(key, &providers.ChatResponse{ID: "x"})
	if got, ok := c.Get(Key("m", []providers.Message{{Role: "user", Content: "hi"}})); !ok || got.ID != "x" {
		t.Fatal("expected hit for identical request")
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get(key); ok {
		t.Fatal("entry should have expired")
	}
}
