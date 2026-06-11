package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// openAICompat talks to any OpenAI-compatible chat-completions API. Groq and
// Ollama's /v1 endpoint are both configurations of it.
type openAICompat struct {
	name, baseURL, apiKey, model string
	// streamUsage asks for a final usage chunk via stream_options. Groq reports
	// stream usage in x_groq instead, so it leaves this off.
	streamUsage bool
}

type openAIRequest struct {
	ChatRequest
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	XGroq *struct {
		Usage *Usage `json:"usage"`
	} `json:"x_groq"`
}

func (p *openAICompat) Name() string { return p.name }

func (p *openAICompat) headers() map[string]string {
	if p.apiKey == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

func (p *openAICompat) body(req ChatRequest, stream bool) openAIRequest {
	if req.Model == "" {
		req.Model = p.model
	}
	req.Stream = stream
	out := openAIRequest{ChatRequest: req}
	if stream && p.streamUsage {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return out
}

func (p *openAICompat) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	resp, err := do(ctx, p.name, http.MethodPost, p.baseURL+"/chat/completions", p.headers(), p.body(req, false))
	if err != nil {
		return nil, err
	}
	var out ChatResponse
	if err := decode(resp, &out); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", p.name, err)
	}
	return &out, nil
}

func (p *openAICompat) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	resp, err := do(ctx, p.name, http.MethodPost, p.baseURL+"/chat/completions", p.headers(), p.body(req, true))
	if err != nil {
		return nil, err
	}
	return streamSSE(ctx, resp.Body, func(data []byte) (StreamChunk, error) {
		var c openAIChunk
		if err := json.Unmarshal(data, &c); err != nil {
			return StreamChunk{}, fmt.Errorf("%s: bad stream chunk: %w", p.name, err)
		}
		out := StreamChunk{Model: c.Model, Usage: c.Usage}
		if len(c.Choices) > 0 {
			out.Content = c.Choices[0].Delta.Content
			if fr := c.Choices[0].FinishReason; fr != nil {
				out.FinishReason = *fr
			}
		}
		if out.Usage == nil && c.XGroq != nil {
			out.Usage = c.XGroq.Usage
		}
		return out, nil
	}), nil
}

func (p *openAICompat) Ping(ctx context.Context) error {
	resp, err := do(ctx, p.name, http.MethodGet, p.baseURL+"/models", p.headers(), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
