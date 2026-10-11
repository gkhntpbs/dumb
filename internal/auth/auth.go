// Package auth defines how the server identifies who is calling it.
//
// The production implementation (pairing, storage) lives outside this public
// repository; StaticTokens is a reference implementation that is safe to read:
// it keeps only SHA-256 hashes of tokens, never the tokens themselves.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// Role separates the two audiences of the server.
type Role string

const (
	RoleWorker Role = "worker"
	RoleCaller Role = "caller"
)

// Principal is an authenticated identity.
type Principal struct {
	ID   string
	Role Role
}

// ErrUnauthorized covers missing, unknown and revoked credentials alike, so a
// response never tells an attacker which one it was.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator maps a bearer token to a Principal.
type Authenticator interface {
	Authenticate(token string) (Principal, error)
}

// HashToken is the only form in which a token is stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type entry struct {
	p       Principal
	revoked bool
}

// StaticTokens is a revocable, in-memory token table keyed by token hash.
// Tokens must be high-entropy random strings; a plain hash is enough for those.
type StaticTokens struct {
	mu     sync.RWMutex
	byHash map[string]*entry
	byID   map[string]string // principal ID -> hash
}

func NewStaticTokens() *StaticTokens {
	return &StaticTokens{byHash: map[string]*entry{}, byID: map[string]string{}}
}

// AddHash registers a principal by the hash of its token. Re-adding an ID
// replaces its previous token.
func (s *StaticTokens) AddHash(id string, role Role, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byID[id]; ok {
		delete(s.byHash, old)
	}
	s.byHash[hash] = &entry{p: Principal{ID: id, Role: role}}
	s.byID[id] = hash
}

// Add registers a principal by its token; only the hash is kept.
func (s *StaticTokens) Add(id string, role Role, token string) {
	s.AddHash(id, role, HashToken(token))
}

// Revoke makes the principal's token fail from now on. It reports whether the ID was known.
func (s *StaticTokens) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.byID[id]
	if ok {
		s.byHash[h].revoked = true
	}
	return ok
}

func (s *StaticTokens) Authenticate(token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.byHash[HashToken(token)]
	if !ok || e.revoked {
		return Principal{}, ErrUnauthorized
	}
	return e.p, nil
}
