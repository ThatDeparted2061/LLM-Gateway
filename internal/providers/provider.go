// Package providers adapts upstream LLM APIs (Groq, Gemini, Ollama) to one
// OpenAI-shaped interface.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider is one upstream LLM API.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// ChatStream returns once the upstream has accepted the request; errors after
	// that arrive as a StreamChunk with Err set. The channel is closed at the end
	// of the stream or when ctx is cancelled.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
	Name() string
	// Ping is a cheap reachability + auth check used by /health.
	Ping(ctx context.Context) error
}

// Message is one chat turn. Only text content is supported.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the subset of the OpenAI chat-completions request the gateway supports.
type ChatRequest struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is an OpenAI chat.completion object.
type ChatResponse struct {
	ID       string   `json:"id"`
	Object   string   `json:"object"`
	Created  int64    `json:"created"`
	Model    string   `json:"model"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
	Provider string   `json:"-"` // which upstream answered; set by the router
}

// StreamChunk is one increment of a streamed completion. Usage, when the
// upstream reports it, arrives on the last chunks.
type StreamChunk struct {
	Model        string
	Content      string
	FinishReason string
	Usage        *Usage
	Err          error
}

// StatusError is a non-2xx answer from an upstream API.
type StatusError struct {
	Provider string
	Code     int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: upstream returned %d: %s", e.Provider, e.Code, e.Body)
}

// Retryable reports whether the same request may succeed later.
func (e *StatusError) Retryable() bool {
	return e.Code == http.StatusTooManyRequests || e.Code >= 500
}

// NewID returns an OpenAI-style completion ID.
func NewID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// httpClient has no overall Timeout because that would cut off long streams.
// Requests are bounded by their ctx, and the transport caps the wait for headers.
var httpClient = &http.Client{Transport: func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}()}

// do sends body as JSON (nil body = no body) and returns the response if it is
// 2xx. Any other status is turned into a *StatusError.
func do(ctx context.Context, provider, method, url string, headers map[string]string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &StatusError{Provider: provider, Code: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	return resp, nil
}

// decode reads a JSON body into v and closes it.
func decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// streamSSE turns an SSE body into StreamChunks: each "data:" payload goes
// through parse, and "[DONE]" ends the stream. The goroutine owns body and stops
// when the body ends, parse fails, or ctx is cancelled.
func streamSSE(ctx context.Context, body io.ReadCloser, parse func(data []byte) (StreamChunk, error)) <-chan StreamChunk {
	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer body.Close()
		send := func(c StreamChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
			if !ok {
				continue // blank separators, comments, "event:" lines
			}
			data = bytes.TrimSpace(data)
			if string(data) == "[DONE]" {
				return
			}
			chunk, err := parse(data)
			if err != nil {
				send(StreamChunk{Err: err})
				return
			}
			if !send(chunk) {
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			send(StreamChunk{Err: err})
		}
	}()
	return ch
}
