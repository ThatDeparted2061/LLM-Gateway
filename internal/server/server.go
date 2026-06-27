// Package server builds the gateway's HTTP routes.
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// New returns the chi router serving the gateway API.
func New(chat http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	r.Method(http.MethodPost, "/v1/chat/completions", chat)
	return r
}
