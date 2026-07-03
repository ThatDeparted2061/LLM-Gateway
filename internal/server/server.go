// Package server builds the gateway's HTTP routes.
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New returns the chi router serving the gateway API.
func New(chat http.Handler, health http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	r.Method(http.MethodPost, "/v1/chat/completions", chat)
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())
	r.Get("/health", health)
	return r
}
