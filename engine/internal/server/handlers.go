package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/steveharsant/tone/engine/internal/catalog"
	"github.com/steveharsant/tone/engine/internal/check"
	"github.com/steveharsant/tone/engine/internal/config"
	"github.com/steveharsant/tone/engine/internal/ollama"
	"github.com/steveharsant/tone/engine/internal/pairing"
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
	// Stream switches the response to NDJSON priority tiers: one line per
	// completed pass ({"tier","suggestions"}), then {"done":true}.
	Stream bool `json:"stream,omitempty"`
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
	s.mu.RLock()
	opts := check.Options{
		Spelling:      s.cfg.Checks.Spelling,
		Grammar:       s.cfg.Checks.Grammar,
		Clarity:       s.cfg.Checks.Clarity,
		Vocabulary:    s.cfg.Checks.Vocabulary,
		Tone:          s.cfg.Checks.Tone,
		ToneTarget:    s.cfg.ToneTarget,
		StyleRules:    append([]string(nil), s.cfg.StyleRules...),
		DisabledRules: append([]string(nil), s.cfg.DisabledRules...),
	}
	s.mu.RUnlock()

	chk, err := s.checker()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	if req.Stream || r.URL.Query().Get("stream") == "1" {
		out := newNDJSON(w)
		err := chk.CheckTiered(r.Context(), req.Text, opts, func(tier string, sugs []check.Suggestion, stats check.Stats) {
			out.send(map[string]any{"tier": tier, "suggestions": sugs, "stats": stats})
		})
		if err != nil && r.Context().Err() == nil {
			out.send(map[string]any{"error": "provider error: " + err.Error()})
			return
		}
		out.send(map[string]any{"done": true})
		return
	}

	sugs, stats, err := chk.Check(r.Context(), req.Text, opts)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "provider error: "+err.Error())
		return
	}
	// Optional per-request narrowing (kept for API compatibility).
	if len(req.Categories) > 0 {
		want := make(map[string]bool, len(req.Categories))
		for _, c := range req.Categories {
			want[c] = true
		}
		filtered := sugs[:0]
		for _, sg := range sugs {
			if want[sg.Category] {
				filtered = append(filtered, sg)
			}
		}
		sugs = filtered
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
	})
}

// --- settings API ------------------------------------------------------

type settingsPayload struct {
	Checks        config.Checks `json:"checks"`
	ToneTarget    string        `json:"tone_target"`
	StyleRules    []string      `json:"style_rules"`
	DisabledRules []string      `json:"disabled_rules"`
	Model         string        `json:"model,omitempty"`
}

var validToneTargets = map[string]bool{
	"": true, "formal": true, "casual": true, "confident": true,
	"friendly": true, "academic": true,
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"checks":         s.cfg.Checks,
		"tone_target":    s.cfg.ToneTarget,
		"style_rules":    s.cfg.StyleRules,
		"disabled_rules": s.cfg.DisabledRules,
		"provider":       s.cfg.Provider,
		"listen_host":    s.cfg.ListenHost,
		"port":           s.cfg.Port,
	})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var p settingsPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if !validToneTargets[p.ToneTarget] {
		writeErr(w, http.StatusBadRequest, "invalid tone_target")
		return
	}
	if len(p.StyleRules) > 50 || len(p.DisabledRules) > 100 {
		writeErr(w, http.StatusBadRequest, "too many rules")
		return
	}
	s.mu.Lock()
	s.cfg.Checks = p.Checks
	s.cfg.ToneTarget = p.ToneTarget
	s.cfg.StyleRules = cleanLines(p.StyleRules, 200)
	s.cfg.DisabledRules = cleanLines(p.DisabledRules, 60)
	if p.Model != "" {
		s.cfg.Provider.Model = p.Model
	}
	err := s.cfg.Save()
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cleanLines(in []string, maxLen int) []string {
	var out []string
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > maxLen {
			l = l[:maxLen]
		}
		out = append(out, l)
	}
	return out
}

// --- pairing API -------------------------------------------------------

func (s *Server) handlePairRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	id, err := s.pairings.Create(req.Client)
	if err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *Server) handlePairPoll(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	status := s.pairings.Poll(id)
	resp := map[string]any{"status": status}
	if status == pairing.StatusApproved {
		s.mu.RLock()
		resp["token"] = s.cfg.PairingToken
		resp["port"] = s.cfg.Port
		s.mu.RUnlock()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePairPending(w http.ResponseWriter, r *http.Request) {
	p := s.pairings.Pending()
	if p == nil {
		p = []pairing.Request{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": p})
}

func (s *Server) handlePairDecide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Approve bool   `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "body must be {\"id\":\"...\",\"approve\":bool}")
		return
	}
	var ok bool
	if req.Approve {
		ok = s.pairings.Approve(req.ID)
	} else {
		ok = s.pairings.Deny(req.ID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": ok})
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

// handlePull starts a model download as a detached background job — closing
// the wizard tab must never abort a multi-GB pull. The wizard polls
// handlePullStatus for progress.
func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "body must be {\"model\":\"tag\"}")
		return
	}

	s.pullMu.Lock()
	defer s.pullMu.Unlock()
	if s.pull.Active {
		if s.pull.Model == req.Model {
			writeJSON(w, http.StatusAccepted, map[string]any{"started": false, "already_running": true})
			return
		}
		writeErr(w, http.StatusConflict, "another model is already downloading: "+s.pull.Model)
		return
	}
	s.pull = pullState{Active: true, Model: req.Model, Phase: "starting"}

	go func(model string) {
		// Deliberately NOT the request context: the job outlives the tab.
		err := s.mgr.Pull(context.Background(), model, func(p ollama.PullProgress) {
			s.pullMu.Lock()
			s.pull.Phase = p.Status
			s.pull.Completed = p.Completed
			s.pull.Total = p.Total
			s.pullMu.Unlock()
		})
		s.pullMu.Lock()
		s.pull.Active = false
		if err != nil {
			s.pull.Phase = "error"
			s.pull.Error = err.Error()
		} else {
			s.pull.Phase = "success"
		}
		s.pullMu.Unlock()
	}(req.Model)

	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func (s *Server) handlePullStatus(w http.ResponseWriter, r *http.Request) {
	s.pullMu.Lock()
	defer s.pullMu.Unlock()
	writeJSON(w, http.StatusOK, s.pull)
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
		s.cfg.Checks = config.Checks{Spelling: true, Grammar: true}
	}
	err := s.cfg.Save()
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
