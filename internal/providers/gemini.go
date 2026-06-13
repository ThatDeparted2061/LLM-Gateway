package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// gemini adapts Google's Generative Language API (generativelanguage.googleapis.com)
// to the OpenAI shape.
type gemini struct{ baseURL, apiKey, model string }

// NewGemini returns the Gemini adapter. baseURL is normally
// https://generativelanguage.googleapis.com/v1beta.
func NewGemini(baseURL, apiKey, model string) Provider {
	return &gemini{baseURL: baseURL, apiKey: apiKey, model: model}
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// toGemini maps OpenAI messages to Gemini: system messages become
// systemInstruction, "assistant" becomes "model", everything else is "user".
func toGemini(req ChatRequest) geminiRequest {
	var out geminiRequest
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if out.SystemInstruction == nil {
				out.SystemInstruction = &geminiContent{}
			}
			out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, geminiPart{m.Content})
		case "assistant":
			out.Contents = append(out.Contents, geminiContent{Role: "model", Parts: []geminiPart{{m.Content}}})
		default:
			out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: []geminiPart{{m.Content}}})
		}
	}
	if req.Temperature != nil || req.MaxTokens > 0 {
		out.GenerationConfig = &geminiGenConfig{Temperature: req.Temperature, MaxOutputTokens: req.MaxTokens}
	}
	return out
}

func (r *geminiResponse) text() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, p := range r.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// finishReason maps Gemini's enum to OpenAI's finish_reason values.
func (r *geminiResponse) finishReason() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	switch fr := r.Candidates[0].FinishReason; fr {
	case "", "FINISH_REASON_UNSPECIFIED":
		return ""
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default:
		return strings.ToLower(fr)
	}
}

func (r *geminiResponse) usage() *Usage {
	if r.UsageMetadata == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     r.UsageMetadata.PromptTokenCount,
		CompletionTokens: r.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      r.UsageMetadata.TotalTokenCount,
	}
}

func (g *gemini) Name() string { return "gemini" }

// headers sends the key as a header rather than ?key= so it never lands in access logs.
func (g *gemini) headers() map[string]string { return map[string]string{"x-goog-api-key": g.apiKey} }

func (g *gemini) modelFor(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return g.model
}

func (g *gemini) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := g.modelFor(req)
	resp, err := do(ctx, "gemini", http.MethodPost, g.baseURL+"/models/"+model+":generateContent", g.headers(), toGemini(req))
	if err != nil {
		return nil, err
	}
	var gr geminiResponse
	if err := decode(resp, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}
	out := &ChatResponse{
		ID:      NewID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: gr.text()},
			FinishReason: gr.finishReason(),
		}},
	}
	if u := gr.usage(); u != nil {
		out.Usage = *u
	}
	return out, nil
}

func (g *gemini) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	model := g.modelFor(req)
	resp, err := do(ctx, "gemini", http.MethodPost, g.baseURL+"/models/"+model+":streamGenerateContent?alt=sse", g.headers(), toGemini(req))
	if err != nil {
		return nil, err
	}
	return streamSSE(ctx, resp.Body, func(data []byte) (StreamChunk, error) {
		var gr geminiResponse
		if err := json.Unmarshal(data, &gr); err != nil {
			return StreamChunk{}, fmt.Errorf("gemini: bad stream chunk: %w", err)
		}
		return StreamChunk{Model: model, Content: gr.text(), FinishReason: gr.finishReason(), Usage: gr.usage()}, nil
	}), nil
}

func (g *gemini) Ping(ctx context.Context) error {
	resp, err := do(ctx, "gemini", http.MethodGet, g.baseURL+"/models/"+g.model, g.headers(), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
