// Package metrics defines the gateway's Prometheus metrics. They live on the
// default registry and are served at /metrics.
package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_requests_total",
		Help: "Upstream provider calls by provider and outcome: ok, the upstream HTTP status, canceled (client went away), or error.",
	}, []string{"provider", "status"})

	// p50/p99: histogram_quantile(0.99, sum by (le, provider) (rate(llm_request_duration_seconds_bucket[5m])))
	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_request_duration_seconds",
		Help:    "Upstream call latency by provider (time to first byte for streams).",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 60, 120}, // local models under load can take minutes
	}, []string{"provider"})

	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_cache_hits_total",
		Help: "Requests answered from cache, by cache type (exact, semantic).",
	}, []string{"cache_type"})

	tokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_tokens_used_total",
		Help: "Upstream tokens consumed, by client API key (hashed) and provider. Cache hits cost none.",
	}, []string{"api_key", "provider"})
)

// ObserveCall records one upstream attempt.
func ObserveCall(provider string, err error, d time.Duration) {
	requests.WithLabelValues(provider, status(err)).Inc()
	duration.WithLabelValues(provider).Observe(d.Seconds())
}

func CacheHit(cacheType string) { cacheHits.WithLabelValues(cacheType).Inc() }

func AddTokens(apiKey, provider string, n int) {
	if n > 0 {
		tokens.WithLabelValues(KeyID(apiKey), provider).Add(float64(n))
	}
}

// KeyID is a short, non-reversible label for an API key, so raw keys never
// show up on the unauthenticated /metrics endpoint.
func KeyID(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:4])
}

func status(err error) string {
	var se *providers.StatusError
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &se):
		return strconv.Itoa(se.Code)
	default:
		return "error"
	}
}
