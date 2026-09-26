// Package server wires configuration, the account pool, upstream client and
// the HTTP API (OpenAI compatible plus the management panel).
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/apikeys"
	"github.com/WWDELE114514/DeepSider2API/internal/config"
	"github.com/WWDELE114514/DeepSider2API/internal/login"
	"github.com/WWDELE114514/DeepSider2API/internal/pool"
	"github.com/WWDELE114514/DeepSider2API/internal/sign"
	"github.com/WWDELE114514/DeepSider2API/internal/upstream"
)

//go:embed panel.html
var panelHTML []byte

// Server is the gateway HTTP application.
type Server struct {
	cfg      *config.Store
	signer   *sign.Signer
	upstream *upstream.Client
	pool     *pool.Pool
	keys     *apikeys.Store
	loginMgr *login.Manager
	stats    *Stats
	started  time.Time
	mux      *http.ServeMux
	filesDir string
	http     *http.Client
}

// New builds the server and loads persisted state.
func New(cfg *config.Store, signer *sign.Signer) (*Server, error) {
	snap := cfg.Get()
	base := cfg.BaseDir()
	dataDir := resolvePath(base, snap.DataDir)
	accountsPath := filepath.Join(dataDir, "accounts.json")
	keysPath := filepath.Join(dataDir, "keys.json")

	p := pool.New(accountsPath, snap.Pool.BreakerThreshold, parseDuration(snap.Pool.BreakerCooldown, 30*time.Minute))
	if err := p.Load(); err != nil {
		return nil, err
	}
	ks := apikeys.New(keysPath)
	if err := ks.Load(); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:      cfg,
		signer:   signer,
		upstream: upstream.New(snap.Upstream, signer),
		pool:     p,
		keys:     ks,
		stats:    NewStats(),
		started:  time.Now(),
		mux:      http.NewServeMux(),
		filesDir: filepath.Join(dataDir, "images"),
		http:     &http.Client{Timeout: 60 * time.Second},
	}
	s.cleanFiles()
	s.loginMgr = login.New(cfg, func(res login.Result) error {
		if existing, ok := p.FindByEmail(res.Email); ok {
			p.SetTokens(existing.ID, res.Token, res.RefreshToken)
			go s.refreshAccount(context.Background(), existing.ID)
			return nil
		}
		acc, err := p.AddFull(res.Email, res.Token, res.RefreshToken, res.Email)
		if err != nil {
			return err
		}
		go s.refreshAccount(context.Background(), acc.ID)
		return nil
	})
	s.routes()
	return s, nil
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return fallback
}

// resolvePath resolves a possibly relative data path against base.
func resolvePath(base, p string) string {
	if p == "" {
		return base
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// Handler exposes the underlying http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) maxAttempts() int {
	n := len(s.pool.List())
	if n <= 0 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("GET /files/{name}", s.handleFile)
	s.mux.HandleFunc("GET /v1/models", s.auth(s.handleModels))
	s.mux.HandleFunc("POST /v1/chat/completions", s.auth(s.handleChat))
	s.mux.HandleFunc("POST /v1/messages", s.auth(s.handleMessages))
	s.mux.HandleFunc("POST /v1/responses", s.auth(s.handleResponses))
	s.mux.HandleFunc("POST /v1/images/generations", s.auth(s.handleImageGenerations))

	s.mux.HandleFunc("GET /api/panel/stats", s.admin(s.handleStats))
	s.mux.HandleFunc("GET /api/panel/accounts", s.admin(s.handleListAccounts))
	s.mux.HandleFunc("POST /api/panel/accounts", s.admin(s.handleAddAccount))
	s.mux.HandleFunc("POST /api/panel/accounts/{id}/toggle", s.admin(s.handleToggleAccount))
	s.mux.HandleFunc("DELETE /api/panel/accounts/{id}", s.admin(s.handleDeleteAccount))
	s.mux.HandleFunc("POST /api/panel/accounts/{id}/refresh", s.admin(s.handleRefreshAccount))
	s.mux.HandleFunc("GET /api/panel/invitation", s.admin(s.handleInvitation))

	s.mux.HandleFunc("POST /api/panel/login/start", s.admin(s.handleLoginStart))
	s.mux.HandleFunc("GET /api/panel/login/status", s.admin(s.handleLoginStatus))
	s.mux.HandleFunc("POST /api/panel/login/cancel", s.admin(s.handleLoginCancel))

	s.mux.HandleFunc("GET /api/panel/keys", s.admin(s.handleListKeys))
	s.mux.HandleFunc("POST /api/panel/keys", s.admin(s.handleCreateKey))
	s.mux.HandleFunc("POST /api/panel/keys/{id}/toggle", s.admin(s.handleToggleKey))
	s.mux.HandleFunc("DELETE /api/panel/keys/{id}", s.admin(s.handleDeleteKey))

	s.mux.HandleFunc("GET /api/panel/config", s.admin(s.handleGetConfig))
	s.mux.HandleFunc("POST /api/panel/config", s.admin(s.handleSetConfig))
	s.mux.HandleFunc("GET /api/panel/models", s.admin(s.handlePanelModels))
	s.mux.HandleFunc("POST /api/panel/chat", s.admin(s.handlePanelChat))
	s.mux.HandleFunc("GET /api/panel/logs", s.admin(s.handleLogs))
	s.mux.HandleFunc("DELETE /api/panel/logs", s.admin(s.handleClearLogs))

	s.mux.HandleFunc("GET /panel", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusMovedPermanently)
	})
	s.mux.HandleFunc("GET /panel/", s.handlePanel)
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusFound)
	})
}

func (s *Server) bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func (s *Server) isAdmin(token string) bool {
	apiKey := s.cfg.Get().APIKey
	return apiKey != "" && token == apiKey
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		token := s.bearer(r)
		if token == "" {
			writeOpenAIError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if s.isAdmin(token) {
			next(w, r)
			return
		}
		if key, ok := s.keys.Verify(token); ok {
			s.keys.Touch(key.ID)
			next(w, r)
			return
		}
		writeOpenAIError(w, http.StatusUnauthorized, "invalid api key")
	}
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.isAdmin(s.bearer(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
}

func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(panelHTML)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.upstream.ListModels(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "list models: "+err.Error())
		return
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, m := range models {
		id, _ := m["botId"].(string)
		if id == "" {
			continue
		}
		data = append(data, map[string]interface{}{
			"id":       id,
			"object":   "model",
			"created":  0,
			"owned_by": "deepsider",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	snap := s.stats.Snapshot()
	snap["uptime_seconds"] = int64(time.Since(s.started).Seconds())
	snap["accounts"] = len(s.pool.List())
	snap["keys"] = s.keys.Count()
	writeJSON(w, snap)
}

func maskToken(t string) string {
	if len(t) <= 20 {
		return "***"
	}
	return t[:12] + "..." + t[len(t)-6:]
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	list := s.pool.List()
	out := make([]map[string]interface{}, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]interface{}{
			"id":               a.ID,
			"name":             a.Name,
			"token_preview":    maskToken(a.Token),
			"enabled":          a.Enabled,
			"email":            a.Email,
			"uid":              a.UID,
			"credit_remaining": a.CreditRemain,
			"plan_name":        a.PlanName,
			"last_used":        a.LastUsed,
			"cooldown_until":   a.CooldownUntil,
			"fail_count":       a.FailCount,
			"last_error":       a.LastError,
			"created_at":       a.CreatedAt,
		})
	}
	writeJSON(w, map[string]interface{}{"accounts": out})
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	acc, err := s.pool.Add(body.Name, body.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.refreshAccount(r.Context(), acc.ID)
	writeJSON(w, map[string]interface{}{"id": acc.ID})
}

func (s *Server) handleToggleAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !s.pool.SetEnabled(id, body.Enabled) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !s.pool.Remove(r.PathValue("id")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleRefreshAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.refreshAccount(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) refreshAccount(ctx context.Context, id string) error {
	acc, ok := s.pool.Get(id)
	if !ok {
		return fmt.Errorf("account not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var lastErr error
	if quota, err := s.upstream.RetrieveQuota(ctx, acc.Token); err == nil {
		s.pool.Update(id, func(a *pool.Account) {
			a.CreditRemain = quota.Available
			a.PlanName = quota.PlanName
		})
	} else {
		lastErr = err
	}
	if profile, err := s.upstream.RetrieveProfile(ctx, acc.Token); err == nil {
		s.pool.Update(id, func(a *pool.Account) {
			a.Email = profile.Email
			a.UID = profile.UID
		})
	} else {
		lastErr = err
	}
	return lastErr
}

func (s *Server) handleInvitation(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.pool.Get(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	inv, err := s.upstream.CreateInvitation(ctx, acc.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	stats, _ := s.upstream.InvitationOverview(ctx, acc.Token)

	writeJSON(w, map[string]interface{}{
		"email":          acc.Email,
		"invitation_id":  inv.InvitationID,
		"desc":           inv.Desc,
		"share_tip":      inv.ShareTip,
		"invited_count":  stats.InvitedCount,
		"chat_std_count": stats.ChatStdCount,
		"chat_adv_count": stats.ChatAdvCount,
		"reward_credits": stats.RewardCredits,
	})
}

func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	id, err := s.loginMgr.Start()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{"id": id})
}

func (s *Server) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loginMgr.Get(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{
		"status": sess.Status,
		"email":  sess.Email,
		"error":  sess.Error,
	})
}

func (s *Server) handleLoginCancel(w http.ResponseWriter, r *http.Request) {
	s.loginMgr.Cancel(r.URL.Query().Get("id"))
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"keys": s.keys.List()})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string   `json:"name"`
		Quota     float64  `json:"quota"`
		Models    []string `json:"models"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var expires time.Time
	if body.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, body.ExpiresAt); err == nil {
			expires = t
		}
	}
	plain, key, err := s.keys.Create(body.Name, body.Quota, body.Models, expires)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"key": plain, "id": key.ID})
}

func (s *Server) handleToggleKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !s.keys.SetEnabled(r.PathValue("id"), body.Enabled) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if !s.keys.Remove(r.PathValue("id")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	writeJSON(w, map[string]interface{}{
		"listen":      cfg.Listen,
		"api_key":     cfg.APIKey,
		"upstream":    cfg.Upstream,
		"pool":        cfg.Pool,
		"auto_model":  cfg.AutoModel,
		"model_fallback": cfg.ModelFallback,
	})
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Listen        *string             `json:"listen"`
		APIKey        *string             `json:"api_key"`
		AutoModel     *config.AutoModel   `json:"auto_model"`
		ModelFallback map[string][]string `json:"model_fallback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	err := s.cfg.Update(func(c *config.Config) {
		if body.Listen != nil {
			c.Listen = *body.Listen
		}
		if body.APIKey != nil && *body.APIKey != "" {
			c.APIKey = *body.APIKey
		}
		if body.AutoModel != nil {
			c.AutoModel = *body.AutoModel
		}
		if body.ModelFallback != nil {
			c.ModelFallback = body.ModelFallback
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handlePanelModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.upstream.ListModels(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]interface{}{"models": models})
}

// handlePanelChat streams a test conversation for the management panel. It
// reuses the same model chain and account pool as the public API.
func (s *Server) handlePanelChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string        `json:"model"`
		Kind     string        `json:"kind"`
		Messages []chatMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages is required", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = "auto"
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(v interface{}) {
		b, _ := json.Marshal(v)
		w.Write([]byte("data: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	if req.Kind == "image" {
		prompt := lastUserText(req.Messages)
		if prompt == "" {
			send(map[string]interface{}{"error": "prompt is required"})
			return
		}
		urls, err := s.generateImages(r.Context(), req.Model, prompt, "1k", "1:1")
		if err != nil {
			send(map[string]interface{}{"error": err.Error()})
			return
		}
		items := s.materializeImages(r.Context(), r, urls)
		send(map[string]interface{}{"images": items, "done": true, "model": req.Model})
		return
	}

	text, used, err := s.runChat(r.Context(), req.Model, req.Messages, func(delta string) error {
		send(map[string]interface{}{"delta": delta})
		return nil
	})
	if err != nil && text == "" {
		send(map[string]interface{}{"error": err.Error()})
		return
	}
	send(map[string]interface{}{"done": true, "model": used, "text": text})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"logs": s.stats.Logs()})
}

func (s *Server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	s.stats.ClearLogs()
	writeJSON(w, map[string]interface{}{"ok": true})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
