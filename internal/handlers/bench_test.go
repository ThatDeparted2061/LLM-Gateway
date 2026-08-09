package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkExactCacheHit measures the gateway's own overhead for a cached
// answer: auth, rate limit, JSON decode, SHA-256 key, cache lookup, JSON encode.
func BenchmarkExactCacheHit(b *testing.B) {
	h, _ := newChat(b, 1<<30)                              // effectively unlimited for the benchmark
	if w := post(h, "k", hello); w.Code != http.StatusOK { // warm the cache
		b.Fatal(w.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(hello))
		r.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatal(w.Code)
		}
	}
}
