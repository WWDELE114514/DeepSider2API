// Package login acquires DeepSider credentials interactively: it opens a real
// browser window (incognito), lets the user sign in on the official login page,
// and captures the JWT from the response of the login endpoints by injecting a
// fetch/XHR hook into the page.
package login

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

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

	result, err := capture(ctx, m.cfg.Get().Login)
	if err != nil {
		m.mu.Lock()
		if s.Status != StatusCancelled {
			s.Status = StatusError
			s.Error = err.Error()
		}
		m.mu.Unlock()
		return
	}

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

// hookScript patches fetch/XHR and stores the first captured token pair on
// window.__DS2API_TOKEN__.
const hookScript = `(function(){
  if (window.__DS2API_HOOKED__) return; window.__DS2API_HOOKED__ = true;
  function norm(s){ return String(s == null ? '' : s).replace(/\s/g, ''); }
  function pick(obj){
    try {
      var d = (obj && obj.data) ? obj.data : obj;
      if (!d) return;
      var token = d.token || d.accessToken || d.access_token;
      if (!token) return;
      window.__DS2API_TOKEN__ = {
        token: norm(token),
        refreshToken: norm(d.refreshToken || d.refresh_token || ''),
        email: d.email || ''
      };
    } catch (e) {}
  }
  function isTarget(u){
    if (!u) return false; u = String(u);
    return u.indexOf('/user/login') >= 0
        || u.indexOf('/user/google-onetap-login') >= 0
        || u.indexOf('/user/google-login') >= 0
        || u.indexOf('/user/refreshtoken') >= 0;
  }
  var of = window.fetch;
  if (of) {
    window.fetch = function(input, init){
      var url = (typeof input === 'string') ? input : (input && input.url);
      return of.apply(this, arguments).then(function(res){
        try { if (isTarget(url || (res && res.url))) { res.clone().json().then(pick).catch(function(){}); } } catch (e) {}
        return res;
      });
    };
  }
  var oo = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(m, u){ try { this.__ds2api_url = u; } catch (e) {} return oo.apply(this, arguments); };
  var os = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function(){
    var self = this;
    try {
      this.addEventListener('load', function(){
        try { if (isTarget(self.__ds2api_url || self.responseURL)) { pick(JSON.parse(self.responseText)); } } catch (e) {}
      });
    } catch (e) {}
    return os.apply(this, arguments);
  };
})();`

func capture(ctx context.Context, lc config.Login) (Result, error) {
	dir, err := os.MkdirTemp("", "ds2api-login-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	opts := []chromedp.ExecAllocatorOption{
		chromedp.UserDataDir(dir),
		chromedp.Flag("headless", false),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.WindowSize(1120, 840),
	}
	if lc.ExtensionPath != "" {
		opts = append(opts,
			chromedp.Flag("disable-extensions-except", lc.ExtensionPath),
			chromedp.Flag("load-extension", lc.ExtensionPath),
		)
	}
	if path := browserPath(lc); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	var browserCtx context.Context
	var cancelBrowser context.CancelFunc
	if lc.Incognito {
		// A dedicated incognito browser context is the reliable way to get a
		// private session. Passing --incognito instead breaks chromedp's
		// navigation (the window stays on about:blank).
		browserCtx, cancelBrowser = chromedp.NewContext(allocCtx, chromedp.WithNewBrowserContext())
	} else {
		browserCtx, cancelBrowser = chromedp.NewContext(allocCtx)
	}
	defer cancelBrowser()

	log.Printf("[login] 启动浏览器并打开: %s", lc.Page)
	if err := chromedp.Run(browserCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(hookScript).Do(ctx)
			return err
		}),
		chromedp.Navigate(lc.Page),
	); err != nil {
		return Result{}, fmt.Errorf("打开登录页失败: %w", err)
	}
	log.Printf("[login] 登录页已打开，等待用户完成登录…")

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("登录超时或已取消")
		case <-ticker.C:
			var raw string
			evalCtx, cancelEval := context.WithTimeout(browserCtx, 5*time.Second)
			err := chromedp.Run(evalCtx, chromedp.Evaluate(`JSON.stringify(window.__DS2API_TOKEN__ || null)`, &raw))
			cancelEval()
			if err != nil || raw == "" || raw == "null" {
				continue
			}
			var r Result
			if json.Unmarshal([]byte(raw), &r) == nil && strings.TrimSpace(r.Token) != "" {
				r.Token = strings.TrimSpace(r.Token)
				r.RefreshToken = strings.TrimSpace(r.RefreshToken)
				return r, nil
			}
		}
	}
}

func browserPath(lc config.Login) string {
	candidates := []string{
		lc.BrowserPath,
		os.Getenv("DS2API_BROWSER"),
		// Edge first (DeepSider extension lives in Edge), Chrome as fallback.
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		"/usr/bin/microsoft-edge",
		"/usr/bin/microsoft-edge-stable",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}
