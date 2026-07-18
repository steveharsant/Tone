// Package server exposes Tone's local HTTP API. The listener binds
// 127.0.0.1 only; every state-touching route requires the pairing token; the
// Host header is validated to block DNS-rebinding tricks. Browser pages that
// are NOT the paired extension get nothing useful without the token.
package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/steveharsant/tone/engine/internal/check"
	"github.com/steveharsant/tone/engine/internal/config"
	"github.com/steveharsant/tone/engine/internal/ollama"
	"github.com/steveharsant/tone/engine/internal/pairing"
	"github.com/steveharsant/tone/engine/internal/provider"
	"github.com/steveharsant/tone/engine/internal/store"
)

//go:embed web
var webFS embed.FS

type Server struct {
	Version string

	mu       sync.RWMutex
	cfg      *config.Config
	mgr      *ollama.Manager
	cache    *check.Cache
	pairings *pairing.Store
	memory   *store.Store

	pullMu sync.Mutex
	pull   pullState
}

// pullState tracks the single in-flight model download (a background job so
// the wizard tab can come and go).
type pullState struct {
	Active    bool   `json:"active"`
	Model     string `json:"model,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
	Error     string `json:"error,omitempty"`
}

func New(version string, cfg *config.Config, mgr *ollama.Manager, memory *store.Store) *Server {
	return &Server{
		Version:  version,
		cfg:      cfg,
		mgr:      mgr,
		cache:    check.NewCache(4096),
		pairings: pairing.NewStore(),
		memory:   memory,
	}
}

// filterStored drops suggestions the user has permanently rejected: custom
// dictionary words and previously dismissed edits. Applied after the check
// (and after the cache) so persistence changes never cost re-inference.
func (s *Server) filterStored(sugs []check.Suggestion) []check.Suggestion {
	if s.memory == nil {
		return sugs
	}
	kept := sugs[:0]
	for _, sg := range sugs {
		if s.memory.HasWord(sg.Original) || s.memory.IsDismissed(sg.Category, sg.Original) {
			continue
		}
		kept = append(kept, sg)
	}
	return kept
}

// Pairings exposes the pairing store to the tray UI, which shares the
// process and approves requests directly.
func (s *Server) Pairings() *pairing.Store { return s.pairings }

// SettingsURL is the tokened settings-page URL (tray + startup banner).
func (s *Server) SettingsURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("http://127.0.0.1:%d/#%s", s.cfg.Port, s.cfg.PairingToken)
}

// checker builds a Checker for the current provider config. The cache is
// shared across rebuilds; its keys include the model, so switching models
// never serves stale results.
func (s *Server) checker() (*check.Checker, error) {
	s.mu.RLock()
	p := s.cfg.Provider
	s.mu.RUnlock()

	var prov provider.Provider
	switch p.Type {
	case config.ProviderOllama:
		// Native API, not OpenAI-compat: only /api/chat accepts think:false,
		// which hybrid reasoning models (qwen3) need to answer promptly.
		prov = provider.NewOllamaNative(strings.TrimSuffix(p.BaseURL, "/"))
	default:
		// Cloud providers land in Phase 2 alongside keychain storage.
		return nil, fmt.Errorf("provider %q is not available yet", p.Type)
	}
	if p.Model == "" {
		return nil, fmt.Errorf("no model configured — run setup first")
	}
	return check.New(prov, p.Model, s.cache), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Extension API.
	mux.HandleFunc("POST /v1/check", s.auth(s.handleCheck))
	mux.HandleFunc("GET /v1/health", s.auth(s.handleHealth))

	// Setup/settings API (used by the embedded pages).
	mux.HandleFunc("GET /api/setup/status", s.auth(s.handleSetupStatus))
	mux.HandleFunc("POST /api/setup/ollama/install", s.auth(s.handleOllamaInstall))
	mux.HandleFunc("POST /api/setup/pull", s.auth(s.handlePull))
	mux.HandleFunc("GET /api/setup/pull/status", s.auth(s.handlePullStatus))
	mux.HandleFunc("POST /api/setup/complete", s.auth(s.handleSetupComplete))
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("POST /api/settings", s.auth(s.handleSaveSettings))

	// Editorial memory: mute a rule type, remember dismissals, dictionary.
	mux.HandleFunc("POST /v1/rules/ignore", s.auth(s.handleIgnoreRule))
	mux.HandleFunc("POST /v1/dismissals", s.auth(s.handleAddDismissal))
	mux.HandleFunc("DELETE /v1/dismissals", s.auth(s.handleClearDismissals))
	mux.HandleFunc("GET /v1/dictionary", s.auth(s.handleGetDictionary))
	mux.HandleFunc("POST /v1/dictionary", s.auth(s.handleAddWord))
	mux.HandleFunc("DELETE /v1/dictionary", s.auth(s.handleRemoveWord))

	// Pairing: request/poll are unauthenticated by design (the extension has
	// no token yet); a human approval gates the token handover.
	mux.HandleFunc("POST /api/pair/request", s.handlePairRequest)
	mux.HandleFunc("GET /api/pair/poll", s.handlePairPoll)
	mux.HandleFunc("GET /api/pair/pending", s.auth(s.handlePairPending))
	mux.HandleFunc("POST /api/pair/decide", s.auth(s.handlePairDecide))

	// Embedded UI (no secrets in the static assets themselves).
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, sub, "setup.html")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, sub, "index.html")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))

	return s.hostCheck(mux)
}

func serveEmbedded(w http.ResponseWriter, r *http.Request, sub fs.FS, name string) {
	b, err := fs.ReadFile(sub, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// hostCheck rejects requests whose Host header is not a loopback name,
// closing the DNS-rebinding hole (attacker.com resolving to 127.0.0.1).
// In remote mode (explicit non-loopback listen_host) the check is relaxed —
// clients will address the engine by hostname/IP — and the token remains
// the actual gate on every sensitive route.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		remote := s.cfg.ListenHost != "" && !isLoopback(s.cfg.ListenHost)
		s.mu.RUnlock()
		if remote {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if isLoopback(host) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden host", http.StatusForbidden)
	})
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "[::1]", "::1":
		return true
	}
	return false
}

// auth enforces the pairing token via the Authorization header ONLY.
// Query-string tokens are deliberately not accepted: URLs get written into
// reverse-proxy and access logs, which would leak the token the moment the
// engine sits behind any proxy. The embedded pages carry the token in the
// URL #fragment (never sent over the wire) and attach it as a header.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		want := s.cfg.PairingToken
		s.mu.RUnlock()

		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == r.Header.Get("Authorization") {
			got = "" // no Bearer prefix → no token
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"missing or invalid pairing token"}`))
			return
		}
		next(w, r)
	}
}

// ListenAndServe binds to loopback (or the explicitly configured
// listen_host) and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.mu.RLock()
	host := s.cfg.ListenHost
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", s.cfg.Port))
	s.mu.RUnlock()

	if !isLoopback(host) {
		log.Printf("WARNING: engine exposed on %s — all requests still require the pairing token, but traffic is plain HTTP; only do this on a trusted network (or tunnel via SSH/Tailscale/reverse proxy with TLS)", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	log.Printf("tone engine listening on http://%s", addr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
