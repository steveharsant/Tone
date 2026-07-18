// Package ollama detects, installs (rootless), supervises and talks to a
// local Ollama instance.
//
// User-friendliness contract: if the user has Ollama already (system install
// or running daemon) we use it untouched. If not, the setup wizard downloads
// the official release tarball into Tone's own data directory — no sudo, no
// curl|sh, no system mutation — and the engine runs `ollama serve` as a
// supervised child process.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultBaseURL = "http://127.0.0.1:11434"

type Manager struct {
	BaseURL string
	// Dir is the managed install root (e.g. ~/.local/share/tone/ollama):
	// bin/ollama and lib/ollama live here after Download.
	Dir    string
	client *http.Client

	mu  sync.Mutex
	cmd *exec.Cmd
}

func NewManager(dir string) *Manager {
	return &Manager{
		BaseURL: DefaultBaseURL,
		Dir:     dir,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

type Status struct {
	Running        bool   `json:"running"`
	Version        string `json:"version,omitempty"`
	SystemInstall  bool   `json:"system_install"`  // ollama found on PATH
	ManagedInstall bool   `json:"managed_install"` // rootless copy in Dir
	Supervised     bool   `json:"supervised"`      // this engine spawned it
}

func (m *Manager) Status(ctx context.Context) Status {
	s := Status{}
	if v, ok := m.serverVersion(ctx); ok {
		s.Running = true
		s.Version = v
	}
	if _, err := exec.LookPath("ollama"); err == nil {
		s.SystemInstall = true
	}
	if _, err := os.Stat(m.managedBinary()); err == nil {
		s.ManagedInstall = true
	}
	m.mu.Lock()
	s.Supervised = m.cmd != nil
	m.mu.Unlock()
	return s
}

func (m *Manager) managedBinary() string {
	return filepath.Join(m.Dir, "bin", "ollama")
}

// binaryPath prefers a system install over our managed copy.
func (m *Manager) binaryPath() (string, error) {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p, nil
	}
	if p := m.managedBinary(); fileExists(p) {
		return p, nil
	}
	return "", fmt.Errorf("ollama binary not found (no system install, no managed install in %s)", m.Dir)
}

func (m *Manager) serverVersion(ctx context.Context) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.BaseURL+"/api/version", nil)
	if err != nil {
		return "", false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var out struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return "", false
	}
	return out.Version, true
}

// Start ensures an Ollama server is reachable, spawning a supervised child if
// needed. Idempotent: a server that is already running (ours or the user's)
// is left alone.
func (m *Manager) Start(ctx context.Context) error {
	if _, ok := m.serverVersion(ctx); ok {
		return nil
	}
	bin, err := m.binaryPath()
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.cmd != nil {
		m.mu.Unlock()
		return m.waitReady(ctx)
	}
	logPath := filepath.Join(m.Dir, "ollama.log")
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		m.mu.Unlock()
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	cmd := exec.Command(bin, "serve")
	// NUM_PARALLEL lets Tone's concurrent tier passes (spelling/grammar/
	// clarity/…) actually overlap on the GPU instead of queuing. Applies to
	// the supervised server only; a user-managed Ollama keeps its own config.
	cmd.Env = append(os.Environ(),
		"OLLAMA_HOST=127.0.0.1:11434",
		"OLLAMA_NUM_PARALLEL=3",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Own process group so Stop can take down the runner subprocesses too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		m.mu.Unlock()
		return fmt.Errorf("start ollama: %w", err)
	}
	m.cmd = cmd
	m.mu.Unlock()

	go func() {
		cmd.Wait()
		logFile.Close()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
		}
		m.mu.Unlock()
	}()

	return m.waitReady(ctx)
}

func (m *Manager) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.serverVersion(ctx); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("ollama did not become ready within 30s (see %s)", filepath.Join(m.Dir, "ollama.log"))
}

// Stop terminates a supervised Ollama. A server we did not spawn is never
// touched.
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative pid = whole process group (serve + model runners).
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

type ModelInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Model string `json:"model"`
}

func (m *Manager) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// PullProgress mirrors Ollama's streaming pull status.
type PullProgress struct {
	Status    string `json:"status"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
}

// Pull downloads a model, streaming progress to the callback.
func (m *Manager) Pull(ctx context.Context, model string, progress func(PullProgress)) error {
	body := fmt.Sprintf(`{"model":%q,"stream":true}`, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL+"/api/pull", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Model pulls are long; bypass the manager's short default timeout.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %s: HTTP %d", model, resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var p struct {
			PullProgress
			Error string `json:"error"`
		}
		if err := dec.Decode(&p); err != nil {
			break // EOF or closed stream: final "success" already handled below
		}
		if p.Error != "" {
			return fmt.Errorf("pull %s: %s", model, p.Error)
		}
		if progress != nil {
			progress(p.PullProgress)
		}
		if p.Status == "success" {
			return nil
		}
	}
	return ctx.Err()
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
