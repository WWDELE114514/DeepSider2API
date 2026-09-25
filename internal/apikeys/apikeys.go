// Package apikeys implements the gateway's own API key store (sk-... keys).
// Only a SHA-256 hash and a masked preview are persisted.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Key is a managed gateway credential.
type Key struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Hash      string    `json:"hash"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
	Usage     int64     `json:"usage"`
	Quota     float64   `json:"quota"`
	Models    []string  `json:"models"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Store persists managed keys as JSON.
type Store struct {
	mu   sync.Mutex
	path string
	keys []*Key
}

// New creates a store backed by path.
func New(path string) *Store { return &Store{path: path} }

// Load reads keys from disk.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.keys)
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// Create issues a new key and returns the plaintext (shown only once).
func (s *Store) Create(name string, quota float64, models []string, expiresAt time.Time) (string, Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plain := "sk-" + randomToken(40)
	prefix := plain[:11]
	key := &Key{
		ID:        randomToken(16),
		Name:      name,
		Prefix:    prefix,
		Hash:      hashKey(plain),
		Enabled:   true,
		CreatedAt: time.Now(),
		Quota:     quota,
		Models:    models,
		ExpiresAt: expiresAt,
	}
	s.keys = append(s.keys, key)
	if err := s.saveLocked(); err != nil {
		return "", Key{}, err
	}
	return plain, *key, nil
}

// Verify checks a plaintext key and returns a copy when valid.
func (s *Store) Verify(plain string) (Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashKey(plain)
	now := time.Now()
	for _, k := range s.keys {
		if subtle.ConstantTimeCompare([]byte(k.Hash), []byte(h)) == 1 {
			if !k.Enabled {
				return *k, false
			}
			if !k.ExpiresAt.IsZero() && now.After(k.ExpiresAt) {
				return *k, false
			}
			return *k, true
		}
	}
	return Key{}, false
}

// Touch records usage of a key.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == id {
			k.LastUsed = time.Now()
			k.Usage++
			_ = s.saveLocked()
			return
		}
	}
}

// List returns all keys sorted by creation time.
func (s *Store) List() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetEnabled toggles a key.
func (s *Store) SetEnabled(id string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == id {
			k.Enabled = enabled
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// Remove deletes a key.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.keys {
		if k.ID == id {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// Count returns the number of stored keys.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}
