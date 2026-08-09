package cache

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

func randVec(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = rand.Float32()*2 - 1
	}
	return v
}

// BenchmarkSemanticLookupFull scans a full cache: 10k entries of 768-dim
// vectors (nomic-embed-text's size), the worst case for the linear scan.
func BenchmarkSemanticLookupFull(b *testing.B) {
	s := NewSemantic("", "", 0.95, time.Hour)
	for range maxSemanticEntries {
		s.Store("m", randVec(768), &providers.ChatResponse{})
	}
	q := randVec(768)
	b.ResetTimer()
	for b.Loop() {
		s.Lookup("m", q)
	}
}

func BenchmarkExactKey(b *testing.B) {
	msgs := []providers.Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "What is the capital of France?"}}
	b.ReportAllocs()
	for b.Loop() {
		Key("llama3.2", msgs)
	}
}
