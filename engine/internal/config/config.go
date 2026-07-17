// Package config manages Tone's on-disk configuration.
//
// The config file lives at $XDG_CONFIG_HOME/tone/config.json (0600). Secrets
// such as cloud API keys are NEVER stored here — they belong in the OS
// keychain (Phase 2); the config only ever holds a reference name.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultPort = 8765

	ProviderOllama    = "ollama"
	ProviderOpenAI    = "openai"
	ProviderDeepSeek  = "deepseek"
	ProviderAnthropic = "anthropic"
)

type Provider struct {
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// APIKeyRef names a keychain entry (Phase 2). Never a key value.
	APIKeyRef string `json:"api_key_ref,omitempty"`
}

// Checks are the user-facing toggles. Spelling and grammar both surface as
// the "correctness" wire category; the split exists so they can be switched
// independently.
type Checks struct {
	Spelling   bool `json:"spelling"`
	Grammar    bool `json:"grammar"` // includes punctuation
	Clarity    bool `json:"clarity"`
	Vocabulary bool `json:"vocabulary"` // wire category "engagement"
	Tone       bool `json:"tone"`       // wire category "delivery"
}

type Config struct {
	Port          int      `json:"port"`
	// ListenHost is the bind address. Anything other than loopback exposes
	// the engine to the network — set only deliberately (see -listen).
	ListenHost    string   `json:"listen_host,omitempty"`
	PairingToken  string   `json:"pairing_token"`
	SetupComplete bool     `json:"setup_complete"`
	Provider      Provider `json:"provider"`
	Checks        Checks   `json:"checks"`
	// ToneTarget is the desired voice ("", "formal", "casual", "confident",
	// "friendly", "academic"). Empty means neutral/no target.
	ToneTarget string `json:"tone_target,omitempty"`
	// StyleRules are user-authored instructions injected into the checker
	// prompt, e.g. "Do not use contractions".
	StyleRules []string `json:"style_rules,omitempty"`
	// DisabledRules suppresses suggestions whose rule slug matches, e.g.
	// "wordiness". Matched case-insensitively.
	DisabledRules []string `json:"disabled_rules,omitempty"`

	// Categories is the legacy pre-Checks field, kept only for migration.
	Categories []string `json:"categories,omitempty"`

	path string
}

// EnabledCategories derives the wire categories from the check toggles.
func (c *Config) EnabledCategories() []string {
	var cats []string
	if c.Checks.Spelling || c.Checks.Grammar {
		cats = append(cats, "correctness")
	}
	if c.Checks.Clarity {
		cats = append(cats, "clarity")
	}
	if c.Checks.Vocabulary {
		cats = append(cats, "engagement")
	}
	if c.Checks.Tone {
		cats = append(cats, "delivery")
	}
	return cats
}

func defaultConfig(path string) *Config {
	return &Config{
		Port:         DefaultPort,
		PairingToken: newToken(),
		Provider: Provider{
			Type:    ProviderOllama,
			BaseURL: "http://127.0.0.1:11434",
		},
		Checks: Checks{Spelling: true, Grammar: true, Clarity: true},
		path:   path,
	}
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// DefaultPath returns $XDG_CONFIG_HOME/tone/config.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tone", "config.json"), nil
}

// DataDir returns the per-user data directory used for managed Ollama,
// models and the suggestion store: $XDG_DATA_HOME/tone.
func DataDir() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "tone"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "tone"), nil
}

// Load reads the config at path, creating (and persisting) a default one on
// first run.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := defaultConfig(path)
		if err := c.Save(); err != nil {
			return nil, fmt.Errorf("create default config: %w", err)
		}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{path: path}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	// Migrate pre-Checks configs: derive toggles from the legacy category
	// list, defaulting to the original correctness+clarity behavior.
	if c.Checks == (Checks{}) {
		c.Checks = Checks{Spelling: true, Grammar: true, Clarity: true}
		for _, cat := range c.Categories {
			switch cat {
			case "engagement":
				c.Checks.Vocabulary = true
			case "delivery":
				c.Checks.Tone = true
			}
		}
		c.Categories = nil
		if err := c.Save(); err != nil {
			return nil, err
		}
	}
	if c.PairingToken == "" {
		c.PairingToken = newToken()
		if err := c.Save(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Save writes the config atomically with owner-only permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
