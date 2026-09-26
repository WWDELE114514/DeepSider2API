// Package login acquires DeepSider credentials interactively: it opens an
// embedded webview window, lets the user sign in on the official login page,
// and captures the JWT from the response of the login endpoints via a JS hook.
//
// The browser engine is provided by the platform webview (WebView2 on Windows).
// capture is implemented per platform with build tags.
package login

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
)

// Result is a captured credential set.
type Result struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	Email        string `json:"email"`
}

// Status of a login session.
type Status string

const (
	StatusPending   Status = "pending"
	StatusSuccess   Status = "success"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

// Session tracks one interactive login.
type Session struct {
	ID      string    `json:"id"`
	Status  Status    `json:"status"`
	Email   string    `json:"email"`
	Error   string    `json:"error"`
	Started time.Time `json:"started"`
	cancel  context.CancelFunc
}

// Manager owns login sessions.
type Manager struct {
	cfg      *config.Store
	onResult func(Result) error
	mu       sync.Mutex
	sessions map[string]*Session
}

// New builds a login manager. onResult is invoked with a captured credential.
func New(cfg *config.Store, onResult func(Result) error) *Manager {
	return &Manager{cfg: cfg, onResult: onResult, sessions: map[string]*Session{}}
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Start launches a background interactive login and returns its session id.
func (m *Manager) Start() (string, error) {
	cfg := m.cfg.Get()
	if !cfg.Login.Enabled {
		return "", fmt.Errorf("交互登录已在配置中关闭")
	}
	timeout := time.Duration(cfg.Login.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	id := newID()
	s := &Session{ID: id, Status: StatusPending, Started: time.Now(), cancel: cancel}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go m.run(ctx, s)
	return id, nil
}

// Get returns a copy of a session.
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, false
	}
	return *s, true
}

// Cancel aborts a running login session.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	if s.cancel != nil {
		s.cancel()
	}
	m.mu.Lock()
	s.Status = StatusCancelled
	m.mu.Unlock()
	return true
}

func (m *Manager) run(ctx context.Context, s *Session) {
	defer s.cancel()

	result, err := m.captureSafe(ctx)
	if err != nil {
		log.Printf("[login] 失败: %v", err)
		m.mu.Lock()
		if s.Status != StatusCancelled {
			s.Status = StatusError
			s.Error = truncate(err.Error(), 1500)
		}
		m.mu.Unlock()
		return
	}
	log.Printf("[login] 成功: %s", result.Email)

	if m.onResult != nil {
		if err := m.onResult(result); err != nil {
			m.mu.Lock()
			s.Status = StatusError
			s.Error = err.Error()
			m.mu.Unlock()
			return
		}
	}

	m.mu.Lock()
	s.Status = StatusSuccess
	s.Email = result.Email
	m.mu.Unlock()
}

// captureSafe isolates the browser automation so that any panic is reported
// instead of crashing the whole gateway process.
func (m *Manager) captureSafe(ctx context.Context) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return capture(ctx, m.cfg.Get().Login)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
