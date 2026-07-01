package handlers

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/metrics"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

// stream proxies a provider stream to the client as OpenAI-style SSE, then
// caches the full answer if the stream finished cleanly.
func (h *Chat) stream(w http.ResponseWriter, r *http.Request, apiKey string, req providers.ChatRequest, exactKey string, vec []float32) {
	ctx := r.Context()
	chunks, provider, err := h.Router.ChatStream(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error()) // nothing sent yet, so a normal error still works
		return
	}

	s := startSSE(w, "miss", provider, req.Model)
	var text strings.Builder
	var usage providers.Usage
	var finish string
	defer func() { metrics.AddTokens(apiKey, provider, usage.TotalTokens) }() // tokens are spent even if the stream is cut short
	for c := range chunks {
		if c.Err != nil {
			slog.Warn("upstream stream failed", "provider", provider, "err", c.Err)
			s.fail(c.Err)
			return
		}
		if c.Model != "" {
			s.model = c.Model
		}
		if c.Usage != nil {
			usage = *c.Usage
		}
		if c.Content == "" && c.FinishReason == "" {
			continue
		}
		text.WriteString(c.Content)
		finish = cmp.Or(c.FinishReason, finish)
		if err := s.send(c.Content, c.FinishReason); err != nil {
			return // client went away; returning cancels ctx, which stops the upstream reader
		}
	}
	if ctx.Err() != nil {
		return
	}
	s.done()
	if finish == "" {
		return // upstream ended without a finish reason: don't cache a possibly partial answer
	}
	h.store(exactKey, req.Model, vec, &providers.ChatResponse{
		ID: s.id, Object: "chat.completion", Created: s.created, Model: s.model, Provider: provider,
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: text.String()}, FinishReason: finish}},
		Usage:   usage,
	})
}

// replay sends a cached response as a single-chunk stream.
func replay(w http.ResponseWriter, resp *providers.ChatResponse, cacheStatus string) {
	s := startSSE(w, cacheStatus, resp.Provider, resp.Model)
	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		if s.send(c.Message.Content, "") != nil || s.send("", cmp.Or(c.FinishReason, "stop")) != nil {
			return
		}
	}
	s.done()
}

// sse writes OpenAI chat.completion.chunk events.
type sse struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	id       string
	model    string
	created  int64
	sentRole bool
}

type chunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

func startSSE(w http.ResponseWriter, cacheStatus, provider, model string) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // stop reverse proxies from buffering the stream
	h.Set("X-Cache", cacheStatus)
	h.Set("X-Provider", provider)
	w.WriteHeader(http.StatusOK)
	return &sse{w: w, rc: http.NewResponseController(w), id: providers.NewID(), model: model, created: time.Now().Unix()}
}

func (s *sse) send(content, finishReason string) error {
	c := chunkChoice{Delta: chunkDelta{Content: content}}
	if !s.sentRole {
		c.Delta.Role, s.sentRole = "assistant", true
	}
	if finishReason != "" {
		c.FinishReason = &finishReason
	}
	return s.event(chunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: []chunkChoice{c}})
}

// fail reports a mid-stream upstream error. The 200 status is already sent, so
// the error goes in-band, the way OpenAI does it.
func (s *sse) fail(err error) {
	s.event(map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
}

func (s *sse) done() {
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	s.rc.Flush()
}

func (s *sse) event(v any) error {
	b, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	return s.rc.Flush()
}
