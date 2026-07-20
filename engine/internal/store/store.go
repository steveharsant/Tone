// Package store persists the user's editorial memory: the custom dictionary
// (words Tone must never flag) and dismissed suggestions (a specific edit the
// user rejected, keyed by category+original). Muted rule types live in the
// config's disabled_rules instead — they're user-editable settings.
//
// A JSON file is plenty: volumes are hundreds of entries, and atomic
// write-and-rename matches config.Save's durability.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxDictionary = 5000
	maxDismissed  = 10000
)

type Dismissal struct {
	Category string `json:"category"`
	Original string `json:"original"`
	// Expires makes this a snooze rather than a permanent dismissal.
	// Zero means forever.
	Expires time.Time `json:"expires,omitempty"`
}

func (d Dismissal) expired(now time.Time) bool {
	return !d.Expires.IsZero() && now.After(d.Expires)
}

type data struct {
	Dictionary []string    `json:"dictionary"`
	Dismissed  []Dismissal `json:"dismissed"`
}

type Store struct {
	mu   sync.Mutex
	path string
	d    data
}

// Open loads the store at path, starting empty if the file doesn't exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normWord(w string) string { return strings.ToLower(strings.TrimSpace(w)) }

// AddWord adds a dictionary entry (idempotent).
func (s *Store) AddWord(w string) error {
	w = strings.TrimSpace(w)
	if w == "" || len(w) > 100 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Dictionary {
		if normWord(existing) == normWord(w) {
			return nil
		}
	}
	s.d.Dictionary = append(s.d.Dictionary, w)
	if len(s.d.Dictionary) > maxDictionary {
		s.d.Dictionary = s.d.Dictionary[len(s.d.Dictionary)-maxDictionary:]
	}
	return s.saveLocked()
}

func (s *Store) RemoveWord(w string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Dictionary[:0]
	for _, existing := range s.d.Dictionary {
		if normWord(existing) != normWord(w) {
			kept = append(kept, existing)
		}
	}
	s.d.Dictionary = kept
	return s.saveLocked()
}

func (s *Store) Words() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.d.Dictionary...)
}

// HasWord reports whether w matches a dictionary entry (case-insensitive).
func (s *Store) HasWord(w string) bool {
	n := normWord(w)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Dictionary {
		if normWord(existing) == n {
			return true
		}
	}
	return false
}

// AddDismissal records a rejected suggestion. A non-zero ttl makes it a
// snooze that silently expires; ttl 0 means forever. Re-adding replaces the
// previous entry (so snoozing then dismissing upgrades to permanent).
func (s *Store) AddDismissal(category, original string, ttl time.Duration) error {
	category = strings.TrimSpace(strings.ToLower(category))
	original = strings.TrimSpace(original)
	if category == "" || original == "" || len(original) > 500 {
		return nil
	}
	entry := Dismissal{Category: category, Original: original}
	if ttl > 0 {
		entry.Expires = time.Now().Add(ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Dismissed[:0]
	for _, d := range s.d.Dismissed {
		if !(d.Category == category && d.Original == original) && !d.expired(time.Now()) {
			kept = append(kept, d)
		}
	}
	s.d.Dismissed = append(kept, entry)
	if len(s.d.Dismissed) > maxDismissed {
		s.d.Dismissed = s.d.Dismissed[len(s.d.Dismissed)-maxDismissed:]
	}
	return s.saveLocked()
}

func (s *Store) IsDismissed(category, original string) bool {
	category = strings.TrimSpace(strings.ToLower(category))
	original = strings.TrimSpace(original)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.d.Dismissed {
		if d.Category == category && d.Original == original && !d.expired(now) {
			return true
		}
	}
	return false
}

func (s *Store) DismissedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.Dismissed)
}

// Dismissals returns the current (non-expired) dismissals for display.
func (s *Store) Dismissals() []Dismissal {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Dismissal, 0, len(s.d.Dismissed))
	for _, d := range s.d.Dismissed {
		if !d.expired(now) {
			out = append(out, d)
		}
	}
	return out
}

// RemoveDismissal forgets a single dismissed suggestion.
func (s *Store) RemoveDismissal(category, original string) error {
	category = strings.TrimSpace(strings.ToLower(category))
	original = strings.TrimSpace(original)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Dismissed[:0]
	for _, d := range s.d.Dismissed {
		if !(d.Category == category && d.Original == original) {
			kept = append(kept, d)
		}
	}
	s.d.Dismissed = kept
	return s.saveLocked()
}

func (s *Store) ClearDismissals() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Dismissed = nil
	return s.saveLocked()
}
