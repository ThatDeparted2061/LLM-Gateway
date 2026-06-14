package providers

import "strings"

// NewOllama returns the adapter for a local Ollama server (e.g.
// http://localhost:11434), using its OpenAI-compatible /v1 endpoint. No API key.
func NewOllama(baseURL, model string) Provider {
	return &openAICompat{
		name:        "ollama",
		baseURL:     strings.TrimRight(baseURL, "/") + "/v1",
		model:       model,
		streamUsage: true,
	}
}
