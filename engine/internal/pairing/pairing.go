// Package pairing implements the auto-connect handshake between the browser
// extension and the engine.
//
// Flow: the extension files an unauthenticated request (it has no token yet);
// the user approves it from the settings page or the tray menu — a deliberate
// human step, since anything local can file a request; the extension's poll
// then receives the pairing token exactly once.
package pairing

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	ttl        = 2 * time.Minute
	maxPending = 3
)

type Request struct {
	ID      string    `json:"id"`
	Client  string    `json:"client"`
	Created time.Time `json:"created"`

	approved bool
	denied   bool
}

type Store struct {
	mu   sync.Mutex
	reqs map[string]*Request
	now  func() time.Time
}

func NewStore() *Store {
	return &Store{reqs: make(map[string]*Request), now: time.Now}
}

var ErrTooMany = errors.New("too many pending pairing requests")

// Create files a new pairing request and returns its ID.
func (s *Store) Create(client string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	pending := 0
	for _, r := range s.reqs {
		if !r.approved && !r.denied {
			pending++
		}
	}
	if pending >= maxPending {
		return "", ErrTooMany
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	if client == "" || len(client) > 64 {
		client = "unknown client"
	}
	s.reqs[id] = &Request{ID: id, Client: client, Created: s.now()}
	return id, nil
}

// Pending lists requests awaiting a decision, oldest first.
func (s *Store) Pending() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	var out []Request
	for _, r := range s.reqs {
		if !r.approved && !r.denied {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *Store) Approve(id string) bool { return s.decide(id, true) }
func (s *Store) Deny(id string) bool    { return s.decide(id, false) }

// ApproveOldest approves the oldest pending request (tray one-click path).
func (s *Store) ApproveOldest() bool {
	p := s.Pending()
	if len(p) == 0 {
		return false
	}
	return s.Approve(p[0].ID)
}

func (s *Store) decide(id string, approve bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	r, ok := s.reqs[id]
	if !ok || r.approved || r.denied {
		return false
	}
	if approve {
		r.approved = true
	} else {
		r.denied = true
	}
	return true
}

type PollStatus string

const (
	StatusPending  PollStatus = "pending"
	StatusApproved PollStatus = "approved"
	StatusDenied   PollStatus = "denied"
	StatusUnknown  PollStatus = "unknown" // expired or never existed
)

// Poll reports a request's state. An approved request is consumed: the
// caller receives StatusApproved exactly once, and the token must be handed
// over on that response only.
func (s *Store) Poll(id string) PollStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	r, ok := s.reqs[id]
	switch {
	case !ok:
		return StatusUnknown
	case r.approved:
		delete(s.reqs, id)
		return StatusApproved
	case r.denied:
		delete(s.reqs, id)
		return StatusDenied
	default:
		return StatusPending
	}
}

func (s *Store) expireLocked() {
	cutoff := s.now().Add(-ttl)
	for id, r := range s.reqs {
		if r.Created.Before(cutoff) {
			delete(s.reqs, id)
		}
	}
}
