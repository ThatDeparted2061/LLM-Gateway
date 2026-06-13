package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiMapsRequestAndResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-x:generateContent" || r.Header.Get("x-goog-api-key") != "k" || r.URL.RawQuery != "" {
			t.Errorf("bad upstream call: %s %s", r.URL, r.Header)
		}
		var req geminiRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.SystemInstruction == nil || req.SystemInstruction.Parts[0].Text != "be brief" ||
			len(req.Contents) != 3 || req.Contents[1].Role != "model" || req.GenerationConfig.MaxOutputTokens != 50 {
			t.Errorf("bad mapping: %+v", req)
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Par"},{"text":"is"}]},"finishReason":"MAX_TOKENS"}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`))
	}))
	defer srv.Close()

	resp, err := NewGemini(srv.URL, "k", "gemini-x").Chat(context.Background(), ChatRequest{
		MaxTokens: 50,
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "capital of France?"},
			{Role: "assistant", Content: "Paris."},
			{Role: "user", Content: "again?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := resp.Choices[0]
	if c.Message.Content != "Paris" || c.FinishReason != "length" || resp.Usage.TotalTokens != 5 || resp.Model != "gemini-x" {
		t.Fatalf("bad response: %+v", resp)
	}
}
