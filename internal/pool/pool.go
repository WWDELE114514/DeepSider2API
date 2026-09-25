// Package pool manages the DeepSider account (JWT) pool with round robin
// selection, per-account cooldown and JSON persistence.
package pool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Account is a single DeepSider credential plus its runtime state.
type Account struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Token         string    `json:"token"`
	Enabled       bool      `json:"enabled"`
	Email         string    `json:"email"`
	UID           string    `json:"uid"`
	CreditRemain  float64   `json:"credit_remaining"`
	PlanName      string    `json:"plan_name"`
	LastUsed      time.Time `json:"last_used"`
	CooldownUntil time.Time `json:"cooldown_until"`
	FailCount     int       `json:"fail_count"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
}

// InCooldown reports whether the account is currently cooling down.
func (a *Account) InCooldown(now time.Time) bool {
	return !a.CooldownUntil.IsZero() && now.Before(a.CooldownUntil)
}

// Available reports whether the account can serve a request right now.
func (a *Account) Available(now time.Time) bool {
	return a.Enabled && !a.InCooldown(now)
}

type storeFile struct {
	Accounts []*Account `json:"accounts"`
}

// Pool is a concurrency safe account pool.
type Pool struct {
	mu               sync.Mutex
	path             string
	accounts         []*Account
	rr               int
	breakerThreshold int
	cooldown         time.Duration
}

// New creates a pool persisted at path.
func New(path string, breakerThreshold int, cooldown time.Duration) *Pool {
	if breakerThreshold <= 0 {
		breakerThreshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Minute
	}
	return &Pool{path: path, breakerThreshold: breakerThreshold, cooldown: cooldown}
}

// Load reads accounts from disk. A missing file yields an empty pool.
func (p *Pool) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parse accounts: %w", err)
	}
	p.accounts = sf.Accounts
	return nil
}

// Save writes the pool to disk atomically.
func (p *Pool) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked()
}

func (p *Pool) saveLocked() error {
	if p.path == "" {
		return nil
	}
	if dir := filepath.Dir(p.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(storeFile{Accounts: p.accounts}, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Add inserts a new account.
func (p *Pool) Add(name, token string) (*Account, error) {
	if token == "" {
		return nil, errors.New("token is empty")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	acc := &Account{
		ID:        newID(),
		Name:      name,
		Token:     token,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	p.accounts = append(p.accounts, acc)
	if err := p.saveLocked(); err != nil {
		return nil, err
	}
	return acc, nil
}

// Remove deletes an account by id.
func (p *Pool) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.accounts {
		if a.ID == id {
			p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
			_ = p.saveLocked()
			return true
		}
	}
	return false
}

// SetEnabled toggles the enabled flag.
func (p *Pool) SetEnabled(id string, enabled bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.Enabled = enabled
			if enabled {
				a.CooldownUntil = time.Time{}
				a.FailCount = 0
			}
			_ = p.saveLocked()
			return true
		}
	}
	return false
}

// Update applies fn to the matching account and persists the result.
func (p *Pool) Update(id string, fn func(*Account)) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			fn(a)
			_ = p.saveLocked()
			return true
		}
	}
	return false
}

// List returns a snapshot sorted by creation time.
func (p *Pool) List() []Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns a copy of an account by id.
func (p *Pool) Get(id string) (Account, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			return *a, true
		}
	}
	return Account{}, false
}

// Pick selects the next available account using round robin.
func (p *Pool) Pick() (*Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	n := len(p.accounts)
	if n == 0 {
		return nil, errors.New("account pool is empty")
	}
	for i := 0; i < n; i++ {
		idx := (p.rr + i) % n
		if p.accounts[idx].Available(now) {
			p.rr = (idx + 1) % n
			acc := *p.accounts[idx]
			return &acc, nil
		}
	}
	return nil, errors.New("no available account (all cooling down or disabled)")
}

// MarkSuccess clears failure state after a successful call.
func (p *Pool) MarkSuccess(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.FailCount = 0
			a.LastError = ""
			a.CooldownUntil = time.Time{}
			a.LastUsed = time.Now()
			_ = p.saveLocked()
			return
		}
	}
}

// MarkFailure increments the failure counter and trips a cooldown when the
// breaker threshold is reached.
func (p *Pool) MarkFailure(id string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.ID == id {
			a.FailCount++
			a.LastError = errMsg
			a.LastUsed = time.Now()
			if a.FailCount >= p.breakerThreshold {
				a.CooldownUntil = time.Now().Add(p.cooldown)
			}
			_ = p.saveLocked()
			return
		}
	}
}
