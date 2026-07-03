package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

type down struct{ *echo }

func (down) Name() string               { return "down" }
func (down) Ping(context.Context) error { return errors.New("connection refused") }

func TestHealthDegradedWhenSomeProvidersDown(t *testing.T) {
	w := httptest.NewRecorder()
	Health([]providers.Provider{&echo{}, down{&echo{}}})(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"status":"degraded"`) || !strings.Contains(body, `"error":"connection refused"`) {
		t.Fatalf("%d %s", w.Code, body)
	}

	w = httptest.NewRecorder()
	Health([]providers.Provider{down{&echo{}}})(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("all down: want 503, got %d", w.Code)
	}
}
