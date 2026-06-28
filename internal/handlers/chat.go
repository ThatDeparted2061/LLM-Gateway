// Package handlers serves the gateway's HTTP endpoints.
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/cache"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/ratelimit"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/router"
)

const maxBodyBytes = 1 << 20

// Chat serves the OpenAI-compatible POST /v1/chat/completions:
// auth -> rate limit -> exact cache -> semantic cache -> provider router,
// storing fresh answers in both caches.
type Chat struct {
	Router   *router.Router
	Limiter  *ratelimit.Limiter
	Exact    *cache.Exact
	Semantic *cache.Semantic // nil disables the semantic cache
	APIKeys  map[string]bool // allowed client keys; empty allows any key
}

func (h *Chat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := apiKey(r)
	if len(h.APIKeys) > 0 && !h.APIKeys[key] {
		writeError(w, http.StatusUnauthorized, "invalid or missing API key")
		return
	}
	if !h.Limiter.Allow(key) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded for this API key")
		return
	}

	var req providers.ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	ctx := r.Context()
	exactKey := cache.Key(req.Model, req.Messages)
	if resp, ok := h.Exact.Get(exactKey); ok {
		reply(w, resp, "exact", req.Stream)
		return
	}

	var vec []float32
	if h.Semantic != nil {
		var err error
		if vec, err = h.Semantic.Embed(ctx, cache.PromptText(req.Messages)); err != nil {
			slog.Warn("semantic cache skipped: embedding failed", "err", err)
		} else if resp, score, ok := h.Semantic.Lookup(req.Model, vec); ok {
			slog.Debug("semantic cache hit", "score", score)
			h.Exact.Set(exactKey, resp) // the next identical prompt skips the embedding call
			reply(w, resp, "semantic", req.Stream)
			return
		}
	}

	if req.Stream {
		h.stream(w, r, req, exactKey, vec)
		return
	}
	resp, err := h.Router.Chat(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.store(exactKey, req.Model, vec, resp)
	reply(w, resp, "miss", false)
}

func (h *Chat) store(exactKey, model string, vec []float32, resp *providers.ChatResponse) {
	h.Exact.Set(exactKey, resp)
	if vec != nil {
		h.Semantic.Store(model, vec, resp)
	}
}

// reply writes resp as JSON, or as a one-chunk SSE stream if the client asked
// to stream. X-Cache (exact | semantic | miss) shows clients, and the k6 test,
// where the answer came from.
func reply(w http.ResponseWriter, resp *providers.ChatResponse, cacheStatus string, stream bool) {
	if stream {
		replay(w, resp, cacheStatus)
		return
	}
	w.Header().Set("X-Cache", cacheStatus)
	w.Header().Set("X-Provider", resp.Provider)
	writeJSON(w, http.StatusOK, resp)
}

// apiKey is the client's bearer token, the identity for auth and rate limiting.
func apiKey(r *http.Request) string {
	if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && k != "" {
		return k
	}
	return "anonymous"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// writeError writes an OpenAI-style error body.
func writeError(w http.ResponseWriter, code int, msg string) {
	errType := strings.ReplaceAll(strings.ToLower(http.StatusText(code)), " ", "_")
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": errType, "code": code}})
}
