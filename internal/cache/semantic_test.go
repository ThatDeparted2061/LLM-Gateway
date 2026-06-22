package cache

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

func TestCosine(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{2, 0}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("parallel vectors: %v", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors: %v", got)
	}
}

func TestSemanticLookup(t *testing.T) {
	// Fake Ollama: texts mentioning "France" embed close together, others far away.
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model, Input string }
		json.NewDecoder(r.Body).Decode(&req)
		vec := []float32{0, 1}
		if strings.Contains(req.Input, "France") {
			vec = []float32{1, 0.05}
			if strings.Contains(req.Input, "What's") {
				vec = []float32{1, 0.1}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	defer ollama.Close()

	s := NewSemantic(ollama.URL, "nomic-embed-text", 0.95, time.Hour)
	ctx := context.Background()
	embed := func(q string) []float32 {
		v, err := s.Embed(ctx, PromptText([]providers.Message{{Role: "user", Content: q}}))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	s.Store("m", embed("What is the capital of France?"), &providers.ChatResponse{ID: "paris"})

	if resp, score, ok := s.Lookup("m", embed("What's the capital of France?")); !ok || resp.ID != "paris" {
		t.Fatalf("paraphrase should hit (score %.3f)", score)
	}
	if _, _, ok := s.Lookup("m", embed("Write a poem about Go")); ok {
		t.Fatal("unrelated prompt must miss")
	}
	if _, _, ok := s.Lookup("other-model", embed("What's the capital of France?")); ok {
		t.Fatal("entries must not cross models")
	}
}
