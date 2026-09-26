// Package config holds the runtime configuration of the gateway and its
// JSON backed persistence helpers.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config is the on-disk configuration document (config.json). It is a plain
// value type so it can be copied freely; use Store for concurrency safety.
type Config struct {
	Listen            string              `json:"listen"`
	APIKey            string              `json:"api_key"`
	DataDir           string              `json:"data_dir"`
	AuthDir           string              `json:"auth_dir"`
	StateFile         string              `json:"state_file"`
	AdminPasswordHash string              `json:"admin_password_hash"`
	TrustProxy        bool                `json:"trust_proxy"`
	Upstream          Upstream            `json:"upstream"`
	Pool              Pool                `json:"pool"`
	AutoModel         AutoModel           `json:"auto_model"`
	ModelFallback     map[string][]string `json:"model_fallback"`
	SessionSticky     SessionSticky       `json:"session_sticky"`
	Login             Login               `json:"login"`
}

// Upstream describes how to talk to DeepSider.
type Upstream struct {
	BaseURLs       []string `json:"base_urls"`
	Version        string   `json:"version"`
	Lang           string   `json:"lang"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	IdleSeconds    int      `json:"idle_seconds"`
}

// Pool tunes account scheduling behaviour.
type Pool struct {
	MaxInFlight      int    `json:"max_in_flight"`
	BreakerThreshold int    `json:"breaker_threshold"`
	BreakerCooldown  string `json:"breaker_cooldown"`
}

// AutoModel configures the virtual "auto" model with day/night switching and a
// fallback chain.
type AutoModel struct {
	Enabled      bool     `json:"enabled"`
	VirtualID    string   `json:"virtual_id"`
	DayPrimary   string   `json:"day_primary"`
	NightPrimary string   `json:"night_primary"`
	DayStart     int      `json:"day_start"`
	DayEnd       int      `json:"day_end"`
	Fallback     []string `json:"fallback"`
	OnEmpty      bool     `json:"on_empty"`
}

// SessionSticky keeps a conversation pinned to one account for a while.
type SessionSticky struct {
	Enabled bool   `json:"enabled"`
	TTL     string `json:"ttl"`
}

// Login configures the interactive account acquisition flow. The gateway opens
// an embedded webview window at Page, the user signs in manually, and the
// response of the login endpoint is captured to obtain the JWT.
type Login struct {
	Enabled        bool   `json:"enabled"`
	Page           string `json:"page"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Default returns a configuration with sensible DeepSider defaults.
func Default() Config {
	return Config{
		Listen:  ":7863",
		APIKey:  "change_me",
		DataDir: "./data",
		AuthDir: "./auths",
		Upstream: Upstream{
			BaseURLs: []string{
				"https://api.deepsider.me",
				"https://api2.deepsider.me",
				"https://api5.deepsider.net",
			},
			Version:        "3.2.8",
			Lang:           "zh-CN",
			TimeoutSeconds: 120,
			IdleSeconds:    300,
		},
		Pool: Pool{
			MaxInFlight:      2,
			BreakerThreshold: 3,
			BreakerCooldown:  "30m",
		},
		AutoModel: AutoModel{
			Enabled:   false,
			VirtualID: "auto",
			DayStart:  8,
			DayEnd:    23,
			OnEmpty:   true,
		},
		ModelFallback: map[string][]string{},
		SessionSticky: SessionSticky{Enabled: true, TTL: "30m"},
		Login: Login{
			Enabled:        true,
			Page:           "https://web.deepsider.online",
			TimeoutSeconds: 300,
		},
	}
}

// Store wraps a Config with locking and file persistence.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Load reads config from path, creating a default file when missing.
func Load(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.normalizeLocked()
	return s, nil
}

// Get returns a copy of the current configuration.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Update mutates the configuration under lock and persists it.
func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	fn(&s.cfg)
	s.normalizeLocked()
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

func (s *Store) normalizeLocked() {
	d := Default()
	c := &s.cfg
	if len(c.Upstream.BaseURLs) == 0 {
		c.Upstream.BaseURLs = d.Upstream.BaseURLs
	}
	if c.Upstream.Version == "" {
		c.Upstream.Version = d.Upstream.Version
	}
	if c.Upstream.Lang == "" {
		c.Upstream.Lang = d.Upstream.Lang
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = d.Upstream.TimeoutSeconds
	}
	if c.Upstream.IdleSeconds <= 0 {
		c.Upstream.IdleSeconds = d.Upstream.IdleSeconds
	}
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.DataDir == "" {
		c.DataDir = d.DataDir
	}
	if c.AuthDir == "" {
		c.AuthDir = d.AuthDir
	}
	if c.StateFile == "" {
		c.StateFile = filepath.Join(c.DataDir, "state.json")
	}
	if c.AutoModel.VirtualID == "" {
		c.AutoModel.VirtualID = "auto"
	}
	if c.ModelFallback == nil {
		c.ModelFallback = map[string][]string{}
	}
	if c.Login.Page == "" {
		c.Login.Page = d.Login.Page
	}
	if c.Login.TimeoutSeconds <= 0 {
		c.Login.TimeoutSeconds = d.Login.TimeoutSeconds
	}
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
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
