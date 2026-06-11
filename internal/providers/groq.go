package providers

// NewGroq returns the Groq adapter. Groq serves an OpenAI-compatible API at
// https://api.groq.com/openai/v1, streams over SSE ("data: {...}" events ending
// in "data: [DONE]"), and reports stream token usage in each final chunk's x_groq field.
func NewGroq(baseURL, apiKey, model string) Provider {
	return &openAICompat{name: "groq", baseURL: baseURL, apiKey: apiKey, model: model}
}
