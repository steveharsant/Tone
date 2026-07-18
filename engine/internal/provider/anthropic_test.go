package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicAdapter(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" {
			t.Error("auth headers missing")
		}
		var req struct {
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct{ Role, Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.System == "" {
			t.Error("system prompt must be lifted to the top-level param")
		}
		if req.MaxTokens <= 0 {
			t.Error("max_tokens is required by the Messages API")
		}
		for _, m := range req.Messages {
			if m.Role == "system" {
				t.Error("system role must not appear in messages")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": "hello"}},
		})
	}))
	defer backend.Close()

	p := NewAnthropic(backend.URL, "sk-test")
	got, err := p.Complete(context.Background(), Request{
		Model: "claude-test",
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("content = %q", got)
	}
}
