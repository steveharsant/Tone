package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAICompat speaks the OpenAI chat-completions dialect. Ollama, OpenAI and
// DeepSeek all serve it natively, so one adapter parameterized by base URL and
// credentials covers all three — only Anthropic needs its own translation.
type OpenAICompat struct {
	name    string
	baseURL string // e.g. "http://127.0.0.1:11434/v1" or "https://api.deepseek.com/v1"
	apiKey  string
	client  *http.Client
}

func NewOpenAICompat(name, baseURL, apiKey string) *OpenAICompat {
	return &OpenAICompat{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		// Generous timeout: a cold local model can take a while to load.
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

func (p *OpenAICompat) Name() string { return p.name }

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaRequest struct {
	Model          string      `json:"model"`
	Messages       []oaMessage `json:"messages"`
	Temperature    float64     `json:"temperature"`
	MaxTokens      int         `json:"max_tokens,omitempty"`
	Stream         bool        `json:"stream"`
	ResponseFormat *oaFormat   `json:"response_format,omitempty"`
}

type oaFormat struct {
	Type string `json:"type"`
}

type oaResponse struct {
	Choices []struct {
		Message oaMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAICompat) Complete(ctx context.Context, req Request) (string, error) {
	body := oaRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONMode {
		body.ResponseFormat = &oaFormat{Type: "json_object"}
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, oaMessage(m))
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("%s: read response: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d: %s", p.name, resp.StatusCode, truncate(string(raw), 300))
	}

	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%s: parse response: %w", p.name, err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s: %s", p.name, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: empty choices", p.name)
	}
	return out.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
