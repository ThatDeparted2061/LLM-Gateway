package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

const (
	maxSemanticEntries = 10_000
	embedTimeout       = 5 * time.Second
)

// Semantic answers prompts that mean the same thing as an earlier one. It embeds
// the conversation with Ollama and returns the stored response whose embedding
// has cosine similarity >= threshold.
//
// ponytail: linear scan over at most maxSemanticEntries vectors per lookup.
// Swap in an ANN index (HNSW, pgvector, Qdrant) when it needs to hold far more.
type Semantic struct {
	ollamaURL, model string
	threshold        float64
	ttl              time.Duration

	mu      sync.RWMutex
	entries []semEntry // oldest first
}

type semEntry struct {
	model   string // request model; answers never cross models
	vec     []float32
	resp    *providers.ChatResponse
	expires time.Time
}

func NewSemantic(ollamaURL, embedModel string, threshold float64, ttl time.Duration) *Semantic {
	return &Semantic{ollamaURL: strings.TrimRight(ollamaURL, "/"), model: embedModel, threshold: threshold, ttl: ttl}
}

// PromptText flattens a conversation into the text that gets embedded. The whole
// conversation is used, not just the last turn, so the same question asked in a
// different context doesn't match.
func PromptText(msgs []providers.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Role + ": " + m.Content + "\n")
	}
	return sb.String()
}

// Embed returns text's embedding from Ollama's /api/embed endpoint.
func (s *Semantic) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": s.model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ollamaURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, errors.New("ollama embed: empty embedding")
	}
	return out.Embeddings[0], nil
}

// Lookup returns the most similar live response for model, if its similarity
// reaches the threshold, along with the best score seen.
func (s *Semantic) Lookup(model string, vec []float32) (*providers.ChatResponse, float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var best *providers.ChatResponse
	bestScore := -1.0
	for _, e := range s.entries {
		if e.model != model || now.After(e.expires) {
			continue
		}
		if sc := cosine(vec, e.vec); sc > bestScore {
			best, bestScore = e.resp, sc
		}
	}
	return best, bestScore, best != nil && bestScore >= s.threshold
}

// Store adds an entry, first dropping expired ones and then the oldest if full.
func (s *Semantic) Store(model string, vec []float32, resp *providers.ChatResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.entries = slices.DeleteFunc(s.entries, func(e semEntry) bool { return now.After(e.expires) })
	if n := len(s.entries) - maxSemanticEntries + 1; n > 0 {
		s.entries = slices.Delete(s.entries, 0, n)
	}
	s.entries = append(s.entries, semEntry{model: model, vec: vec, resp: resp, expires: now.Add(s.ttl)})
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
