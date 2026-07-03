package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

type providerHealth struct {
	Status    string `json:"status"` // up | down
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Health serves GET /health: it pings every provider concurrently and reports
// each one's status. Overall status is ok (all up), degraded (some up) or
// down. It returns 503 only when no provider is up, because then the gateway
// can serve nothing but cache hits.
func Health(ps []providers.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		results := make([]providerHealth, len(ps))
		var wg sync.WaitGroup
		for i, p := range ps {
			wg.Go(func() {
				start := time.Now()
				err := p.Ping(ctx)
				results[i] = providerHealth{Status: "up", LatencyMS: time.Since(start).Milliseconds()}
				if err != nil {
					results[i].Status, results[i].Error = "down", err.Error()
				}
			})
		}
		wg.Wait()

		body := struct {
			Status    string                    `json:"status"`
			Providers map[string]providerHealth `json:"providers"`
		}{Status: "down", Providers: map[string]providerHealth{}}
		up := 0
		for i, p := range ps {
			body.Providers[p.Name()] = results[i]
			if results[i].Status == "up" {
				up++
			}
		}
		code := http.StatusOK
		switch {
		case up == len(ps):
			body.Status = "ok"
		case up > 0:
			body.Status = "degraded"
		default:
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, body)
	}
}
