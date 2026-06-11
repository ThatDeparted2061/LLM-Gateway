package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroqStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		var req openAIRequest
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream || req.Model != "default-model" {
			t.Errorf("bad upstream request: %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"x_groq\":{\"usage\":{\"total_tokens\":7}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ch, err := NewGroq(srv.URL, "k", "default-model").ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var last StreamChunk
	for c := range ch {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		text.WriteString(c.Content)
		last = c
	}
	if text.String() != "Hello" || last.FinishReason != "stop" || last.Usage == nil || last.Usage.TotalTokens != 7 {
		t.Fatalf("got %q, last chunk %+v", text.String(), last)
	}
}

func TestGroqStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := NewGroq(srv.URL, "k", "m").Chat(context.Background(), ChatRequest{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 || !se.Retryable() {
		t.Fatalf("want retryable 429 StatusError, got %v", err)
	}
}
