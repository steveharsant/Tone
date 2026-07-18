package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveharsant/tone/engine/internal/config"
	"github.com/steveharsant/tone/engine/internal/ollama"
)

// mockBackend impersonates Ollama: version probe, model list, and an
// OpenAI-compatible chat endpoint that flags "definately" in whatever
// segment it receives.
func mockBackend(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"0.0.0-mock"}`))
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"mock-model:latest","size":1}]}`))
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		content := `{"suggestions":[]}`
		if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "definately") {
			content = `{"suggestions":[{"original":"definately","replacement":"definitely","category":"correctness","rule":"spelling","explanation":"Misspelling of definitely."}]}`
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	})
	return httptest.NewServer(mux)
}

func testServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	backend := mockBackend(t)
	t.Cleanup(backend.Close)

	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Provider.BaseURL = backend.URL
	cfg.Provider.Model = "mock-model"
	cfg.SetupComplete = true

	mgr := ollama.NewManager(filepath.Join(t.TempDir(), "ollama"))
	mgr.BaseURL = backend.URL

	s := New("test", cfg, mgr)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, cfg.PairingToken
}

func TestCheckEndToEndThroughHTTP(t *testing.T) {
	ts, token := testServer(t)

	body := `{"text":"I will definately be there. This part is fine."}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/check", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Suggestions []struct {
			Span        struct{ Start, End int } `json:"span"`
			Original    string                   `json:"original"`
			Replacement string                   `json:"replacement"`
			Category    string                   `json:"category"`
		} `json:"suggestions"`
		Stats struct{ Segments int } `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v", out.Suggestions)
	}
	s := out.Suggestions[0]
	if s.Original != "definately" || s.Replacement != "definitely" || s.Category != "correctness" {
		t.Errorf("unexpected suggestion: %+v", s)
	}
	text := "I will definately be there. This part is fine."
	if text[s.Span.Start:s.Span.End] != "definately" {
		t.Errorf("span covers %q", text[s.Span.Start:s.Span.End])
	}
	if out.Stats.Segments != 2 {
		t.Errorf("segments = %d, want 2", out.Stats.Segments)
	}
}

func TestAuthRequired(t *testing.T) {
	ts, _ := testServer(t)
	resp, err := http.Post(ts.URL+"/v1/check", "application/json", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token → %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token → %d, want 401", resp2.StatusCode)
	}
}

func TestHostHeaderRejected(t *testing.T) {
	ts, token := testServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = "evil.example.com" // DNS-rebinding simulation
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound host → %d, want 403", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	ts, token := testServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h struct {
		Status string `json:"status"`
		Ollama struct {
			Running bool `json:"running"`
		} `json:"ollama"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "ok" || !h.Ollama.Running {
		t.Errorf("health = %+v", h)
	}
}

func TestSetupPagesServeWithoutToken(t *testing.T) {
	ts, _ := testServer(t)
	for _, path := range []string{"/setup", "/", "/static/tone.css"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestQueryStringTokenRejected(t *testing.T) {
	ts, token := testServer(t)
	// Tokens in URLs leak into proxy logs; only the Authorization header works.
	resp, err := http.Get(ts.URL + "/v1/health?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query-string token accepted: %d, want 401", resp.StatusCode)
	}
}
