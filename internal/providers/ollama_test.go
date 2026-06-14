package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaUsesV1AndRequestsStreamUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIRequest
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" ||
			req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Errorf("bad upstream call: %s %+v", r.URL.Path, req)
		}
		w.Write([]byte("data: {\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	ch, err := NewOllama(srv.URL+"/", "llama3.2").ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	c := <-ch
	if c.Err != nil || c.Usage == nil || c.Usage.TotalTokens != 3 {
		t.Fatalf("got %+v", c)
	}
}
