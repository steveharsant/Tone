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

// OllamaNative speaks Ollama's own /api/chat rather than its OpenAI-compat
// endpoint, because only the native API accepts "think": false. Hybrid
// reasoning models (qwen3 et al.) otherwise burn minutes of tokens on hidden
// deliberation and can return an EMPTY content field — fatal for a
// latency-sensitive checker. Cloud providers keep the OpenAICompat adapter.
type OllamaNative struct {
	baseURL string // e.g. "http://127.0.0.1:11434"
	client  *http.Client
}

func NewOllamaNative(baseURL string) *OllamaNative {
	return &OllamaNative{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 180 * time.Second},
	}
}

func (p *OllamaNative) Name() string { return "ollama" }

type olMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type olRequest struct {
	Model    string      `json:"model"`
	Messages []olMessage `json:"messages"`
	Stream   bool        `json:"stream"`
	Think    bool        `json:"think"`
	Format   string      `json:"format,omitempty"`
	Options  struct {
		Temperature float64 `json:"temperature"`
		NumPredict  int     `json:"num_predict,omitempty"`
	} `json:"options"`
}

func (p *OllamaNative) Complete(ctx context.Context, req Request) (string, error) {
	body := olRequest{
		Model:  req.Model,
		Stream: false,
		Think:  false, // a grammar checker wants answers, not deliberation
	}
	body.Options.Temperature = req.Temperature
	body.Options.NumPredict = req.MaxTokens
	if req.JSONMode {
		body.Format = "json"
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, olMessage(m))
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("ollama: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Message olMessage `json:"message"`
		Error   string    `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("ollama: parse response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama: %s", out.Error)
	}
	return out.Message.Content, nil
}
