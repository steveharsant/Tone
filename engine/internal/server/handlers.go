package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/steveharsant/tone/engine/internal/catalog"
	"github.com/steveharsant/tone/engine/internal/check"
	"github.com/steveharsant/tone/engine/internal/ollama"
)

const maxCheckBytes = 256 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- /v1/check ---------------------------------------------------------

type checkRequest struct {
	Text       string   `json:"text"`
	Categories []string `json:"categories,omitempty"`
}

type checkResponse struct {
	Suggestions []check.Suggestion `json:"suggestions"`
	Stats       check.Stats        `json:"stats"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCheckBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusOK, checkResponse{Suggestions: []check.Suggestion{}})
		return
	}
	cats := req.Categories
	if len(cats) == 0 {
		s.mu.RLock()
		cats = append(cats, s.cfg.Categories...)
		s.mu.RUnlock()
	}

	chk, err := s.checker()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	sugs, stats, err := chk.Check(r.Context(), req.Text, cats)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "provider error: "+err.Error())
		return
	}
	if sugs == nil {
		sugs = []check.Suggestion{}
	}
	writeJSON(w, http.StatusOK, checkResponse{Suggestions: sugs, Stats: stats})
}

// --- /v1/health --------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	prov := s.cfg.Provider
	setupDone := s.cfg.SetupComplete
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	st := s.mgr.Status(ctx)

	status := "ok"
	if !setupDone {
		status = "setup_required"
	} else if prov.Type == "ollama" && !st.Running {
		status = "backend_unavailable"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         status,
		"engine_version": s.Version,
		"setup_complete": setupDone,
		"provider":       map[string]string{"type": prov.Type, "model": prov.Model},
		"ollama":         st,
	})
}

// --- setup API ---------------------------------------------------------

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	st := s.mgr.Status(ctx)

	var installed []ollama.ModelInfo
	if st.Running {
		if ms, err := s.mgr.Models(ctx); err == nil {
			installed = ms
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ollama":           st,
		"installed_models": installed,
		"curated":          catalog.Curated,
		"provider":         s.cfg.Provider,
		"setup_complete":   s.cfg.SetupComplete,
		"categories":       s.cfg.Categories,
	})
}

// ndjsonWriter streams one JSON object per line with immediate flushing —
// consumed by the wizard via fetch() + ReadableStream (POST-friendly,
// unlike EventSource).
type ndjsonWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newNDJSON(w http.ResponseWriter) *ndjsonWriter {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &ndjsonWriter{w: w, f: f}
}

func (n *ndjsonWriter) send(v any) {
	json.NewEncoder(n.w).Encode(v)
	if n.f != nil {
		n.f.Flush()
	}
}

// handleOllamaInstall performs the rootless install: download the official
// tarball into the data dir, verify, extract, then start the supervised
// server. Progress streams to the wizard.
func (s *Server) handleOllamaInstall(w http.ResponseWriter, r *http.Request) {
	out := newNDJSON(w)
	st := s.mgr.Status(r.Context())
	if !st.ManagedInstall && !st.SystemInstall {
		err := s.mgr.Download(r.Context(), func(p ollama.DownloadProgress) {
			out.send(map[string]any{"phase": p.Phase, "completed": p.Completed, "total": p.Total})
		})
		if err != nil {
			out.send(map[string]any{"error": err.Error()})
			return
		}
	}
	out.send(map[string]any{"phase": "starting"})
	if err := s.mgr.Start(r.Context()); err != nil {
		out.send(map[string]any{"error": err.Error()})
		return
	}
	out.send(map[string]any{"phase": "done"})
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "body must be {\"model\":\"tag\"}")
		return
	}
	out := newNDJSON(w)
	err := s.mgr.Pull(r.Context(), req.Model, func(p ollama.PullProgress) {
		out.send(map[string]any{"phase": p.Status, "completed": p.Completed, "total": p.Total})
	})
	if err != nil {
		out.send(map[string]any{"error": err.Error()})
		return
	}
	out.send(map[string]any{"phase": "done"})
}

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "body must be {\"model\":\"tag\"}")
		return
	}
	s.mu.Lock()
	s.cfg.Provider.Model = req.Model
	s.cfg.SetupComplete = true
	// The 1.7B fallback can't handle style categories reliably.
	if m, ok := catalog.ByTag(req.Model); ok && m.MinRAMGB <= 3 {
		s.cfg.Categories = []string{"correctness"}
	}
	err := s.cfg.Save()
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
