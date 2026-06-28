package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/cache"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/ratelimit"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/router"
)

// echo answers every prompt with "re: <last message>" and counts calls.
type echo struct{ calls int }

func (e *echo) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	e.calls++
	return &providers.ChatResponse{
		ID: "id", Object: "chat.completion", Model: "echo-1",
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "re: " + req.Messages[len(req.Messages)-1].Content}, FinishReason: "stop"}},
		Usage:   providers.Usage{TotalTokens: 4},
	}, nil
}

func (e *echo) ChatStream(_ context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	e.calls++
	ch := make(chan providers.StreamChunk, 3)
	ch <- providers.StreamChunk{Model: "echo-1", Content: "re: "}
	ch <- providers.StreamChunk{Content: req.Messages[len(req.Messages)-1].Content, FinishReason: "stop"}
	ch <- providers.StreamChunk{Usage: &providers.Usage{TotalTokens: 4}}
	close(ch)
	return ch, nil
}

func (e *echo) Name() string               { return "echo" }
func (e *echo) Ping(context.Context) error { return nil }

func newChat(t *testing.T, burst int) (*Chat, *echo) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &echo{}
	return &Chat{
		Router:  router.New(p),
		Limiter: ratelimit.New(ctx, 0.001, burst),
		Exact:   cache.NewExact(ctx, time.Hour),
	}, p
}

func post(h http.Handler, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const hello = `{"messages":[{"role":"user","content":"hello"}]}`

func TestChatCachesThenRateLimits(t *testing.T) {
	h, p := newChat(t, 2)

	first := post(h, "k", hello)
	if first.Code != 200 || first.Header().Get("X-Cache") != "miss" || !strings.Contains(first.Body.String(), "re: hello") {
		t.Fatalf("first: %d %s %s", first.Code, first.Header(), first.Body)
	}
	second := post(h, "k", hello)
	if second.Code != 200 || second.Header().Get("X-Cache") != "exact" || p.calls != 1 {
		t.Fatalf("second should be an exact hit without calling the provider: %d %s calls=%d", second.Code, second.Header(), p.calls)
	}
	if third := post(h, "k", hello); third.Code != http.StatusTooManyRequests {
		t.Fatalf("third: want 429, got %d", third.Code)
	}
	if other := post(h, "other-key", hello); other.Code != 200 {
		t.Fatalf("other key has its own bucket, got %d", other.Code)
	}
}

func TestChatRejectsUnknownKeyAndBadBody(t *testing.T) {
	h, _ := newChat(t, 10)
	h.APIKeys = map[string]bool{"good": true}

	if w := post(h, "bad", hello); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if w := post(h, "good", `{"messages":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestChatStreamsThenReplaysFromCache(t *testing.T) {
	h, p := newChat(t, 10)
	const body = `{"stream":true,"messages":[{"role":"user","content":"hello"}]}`

	first := post(h, "k", body)
	out := first.Body.String()
	if first.Header().Get("Content-Type") != "text/event-stream" || first.Header().Get("X-Cache") != "miss" ||
		!strings.Contains(out, `"content":"re: "`) || !strings.Contains(out, `"finish_reason":"stop"`) ||
		!strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("bad stream: %s\n%s", first.Header(), out)
	}

	second := post(h, "k", body)
	if second.Header().Get("X-Cache") != "exact" || !strings.Contains(second.Body.String(), `"content":"re: hello"`) || p.calls != 1 {
		t.Fatalf("completed stream should be cached and replayed: %s calls=%d", second.Body, p.calls)
	}
}
