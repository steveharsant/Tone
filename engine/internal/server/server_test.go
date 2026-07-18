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
	"github.com/steveharsant/tone/engine/internal/store"
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
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Think    bool                              `json:"think"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Think {
			// The engine must always disable thinking for checks.
			http.Error(w, `{"error":"mock: think must be false"}`, http.StatusBadRequest)
			return
		}
		content := `{"suggestions":[]}`
		if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "definately") {
			content = `{"suggestions":[{"original":"definately","replacement":"definitely","category":"correctness","rule":"spelling","explanation":"Misspelling of definitely."}]}`
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"role": "assistant", "content": content},
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

	memory, _ := store.Open(filepath.Join(t.TempDir(), "store.json"))
	s := New("test", cfg, mgr, memory)
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

func TestStoredMemoryFiltersSuggestions(t *testing.T) {
	ts, token := testServer(t)
	post := func(path, body string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	check := func() int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/check", strings.NewReader(`{"text":"I will definately be there."}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Suggestions []struct{ Original string } `json:"suggestions"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return len(out.Suggestions)
	}

	if n := check(); n != 1 {
		t.Fatalf("baseline: want 1 suggestion, got %d", n)
	}
	// Dismiss the exact edit → filtered out (cache untouched: same segments).
	if code := post("/v1/dismissals", `{"category":"correctness","original":"definately"}`); code != 200 {
		t.Fatalf("dismissal POST = %d", code)
	}
	if n := check(); n != 0 {
		t.Fatalf("after dismissal: want 0 suggestions, got %d", n)
	}
}

func TestDictionaryFiltersSuggestions(t *testing.T) {
	ts, token := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/dictionary", strings.NewReader(`{"word":"definately"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	checkReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/check", strings.NewReader(`{"text":"I will definately be there."}`))
	checkReq.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(checkReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		Suggestions []any `json:"suggestions"`
	}
	json.NewDecoder(resp2.Body).Decode(&out)
	if len(out.Suggestions) != 0 {
		t.Fatalf("dictionary word still flagged: %d suggestions", len(out.Suggestions))
	}
}

func TestIgnoreRuleEndpoint(t *testing.T) {
	ts, token := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/rules/ignore", strings.NewReader(`{"rule":"spelling"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ignore rule = %d", resp.StatusCode)
	}
	// The mock only emits rule "spelling" → everything now filtered.
	checkReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/check", strings.NewReader(`{"text":"I will definately be there."}`))
	checkReq.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(checkReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		Suggestions []any `json:"suggestions"`
	}
	json.NewDecoder(resp2.Body).Decode(&out)
	if len(out.Suggestions) != 0 {
		t.Fatalf("ignored rule still flagged: %d", len(out.Suggestions))
	}
}

func TestPartialSettingsSaveLeavesOthersUntouched(t *testing.T) {
	ts, token := testServer(t)
	do := func(body string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("save = %d", resp.StatusCode)
		}
	}
	// Establish style rules + checks, then send a provider-only patch.
	do(`{"checks":{"spelling":true,"grammar":true,"clarity":true},"tone_target":"formal","style_rules":["No contractions"],"disabled_rules":["wordiness"]}`)
	do(`{"provider":{"type":"ollama","model":"other-model"}}`)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Checks     struct{ Spelling, Clarity bool } `json:"checks"`
		ToneTarget string                           `json:"tone_target"`
		StyleRules []string                         `json:"style_rules"`
		Provider   struct{ Model string }           `json:"provider"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.Checks.Spelling || !out.Checks.Clarity {
		t.Errorf("provider-only patch wiped checks: %+v", out.Checks)
	}
	if out.ToneTarget != "formal" || len(out.StyleRules) != 1 {
		t.Errorf("provider-only patch wiped tone/style: %q %v", out.ToneTarget, out.StyleRules)
	}
	if out.Provider.Model != "other-model" {
		t.Errorf("provider patch not applied: %q", out.Provider.Model)
	}
}
