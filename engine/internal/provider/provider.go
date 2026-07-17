// Package provider defines the LLM backend abstraction. Every backend —
// local Ollama, OpenAI, DeepSeek, Anthropic — implements Provider; the check
// pipeline is backend-agnostic.
package provider

import "context"

type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	// JSONMode asks the backend to constrain output to a JSON object where
	// supported (OpenAI-compatible response_format json_object).
	JSONMode bool
}

// Provider is the single seam between Tone and any LLM backend.
type Provider interface {
	Name() string
	// Complete returns the assistant message content for the request.
	Complete(ctx context.Context, req Request) (string, error)
}
