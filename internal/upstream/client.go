// Package upstream implements the DeepSider HTTP client used by the gateway.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
	"github.com/WWDELE114514/DeepSider2API/internal/sign"
)

// Client talks to the DeepSider API using a rotating base URL and a shared
// signing module.
type Client struct {
	cfg      config.Upstream
	signer   *sign.Signer
	http     *http.Client
	baseURLs []string
	rr       uint64
}

// New builds a client from configuration.
func New(cfg config.Upstream, signer *sign.Signer) *Client {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &Client{
		cfg:      cfg,
		signer:   signer,
		baseURLs: cfg.BaseURLs,
		http:     &http.Client{Transport: transport},
	}
}

func (c *Client) nextBase() string {
	if len(c.baseURLs) == 0 {
		return "https://api.deepsider.me"
	}
	n := atomic.AddUint64(&c.rr, 1)
	return c.baseURLs[int(n-1)%len(c.baseURLs)]
}

func (c *Client) commonHeaders() map[string]string {
	return map[string]string{
		"i-version": c.cfg.Version,
		"i-lang":    c.cfg.Lang,
	}
}

// APIError describes a non-success response from DeepSider.
type APIError struct {
	Status  int
	Code    int
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("deepsider %d (code %d): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("deepsider http %d: %s", e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ListModels fetches the public model catalogue.
func (c *Client) ListModels(ctx context.Context) ([]map[string]interface{}, error) {
	base := c.nextBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/model/category-list", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			List []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	return parsed.Data.List, nil
}

// Quota describes the credit state of an account.
type Quota struct {
	Available float64 `json:"available"`
	Total     float64 `json:"total"`
	Unit      string  `json:"unit"`
	Title     string  `json:"title"`
	PlanName  string  `json:"plan_name"`
}

type quotaResponse struct {
	Code int `json:"code"`
	Data struct {
		ShowPlanName string `json:"showPlanName"`
		List         []struct {
			Type      string  `json:"type"`
			Total     float64 `json:"total"`
			Available float64 `json:"available"`
			Unit      string  `json:"unit"`
			Title     string  `json:"title"`
		} `json:"list"`
	} `json:"data"`
}

// RetrieveQuota fetches the credit/quota information for a token.
func (c *Client) RetrieveQuota(ctx context.Context, token string) (Quota, error) {
	base := c.nextBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/quota/retrieve", nil)
	if err != nil {
		return Quota{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Quota{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Quota{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed quotaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Quota{}, fmt.Errorf("decode quota: %w", err)
	}
	out := Quota{PlanName: parsed.Data.ShowPlanName}
	for _, item := range parsed.Data.List {
		if item.Type == "free" || out.Total == 0 {
			out.Total = item.Total
			out.Available = item.Available
			out.Unit = item.Unit
			out.Title = item.Title
		}
	}
	return out, nil
}

// Profile is a trimmed view of /api/user/profile.
type Profile struct {
	Email string `json:"email"`
	UID   string `json:"uid"`
}

// RetrieveProfile fetches basic account identity.
func (c *Client) RetrieveProfile(ctx context.Context, token string) (Profile, error) {
	base := c.nextBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/user/profile", nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Profile{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed struct {
		Data struct {
			Email string `json:"email"`
			UID   string `json:"uid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Profile{}, err
	}
	return Profile{Email: parsed.Data.Email, UID: parsed.Data.UID}, nil
}

// Invitation is the per-account invite code and share copy.
type Invitation struct {
	InvitationID string `json:"invitationId"`
	Desc         string `json:"desc"`
	ShareTip     string `json:"shareTip"`
}

// InvitationStats summarises invite progress for an account.
type InvitationStats struct {
	InvitedCount  int `json:"invitedCount"`
	ChatStdCount  int `json:"chatStdCount"`
	ChatAdvCount  int `json:"chatAdvCount"`
	RewardCredits int `json:"rewardCredits"`
}

// CreateInvitation returns the account's invite code (JWT only, no i-sign).
func (c *Client) CreateInvitation(ctx context.Context, token string) (Invitation, error) {
	base := c.nextBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/invitation/create", nil)
	if err != nil {
		return Invitation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Invitation{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Invitation{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed struct {
		Code int        `json:"code"`
		Data Invitation `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Invitation{}, fmt.Errorf("decode invitation: %w", err)
	}
	return parsed.Data, nil
}

// InvitationOverview returns invite statistics (JWT only, no i-sign).
func (c *Client) InvitationOverview(ctx context.Context, token string) (InvitationStats, error) {
	base := c.nextBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/invitation/overview", nil)
	if err != nil {
		return InvitationStats{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return InvitationStats{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return InvitationStats{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed struct {
		Code int             `json:"code"`
		Data InvitationStats `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return InvitationStats{}, fmt.Errorf("decode invitation overview: %w", err)
	}
	return parsed.Data, nil
}

// TokenPair is the result of a refresh-token exchange.
type TokenPair struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	Email        string `json:"email"`
}

// RefreshToken exchanges a refresh token for a fresh JWT.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (TokenPair, error) {
	base := c.nextBase()
	payload, _ := json.Marshal(map[string]string{"refreshtoken": refreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/user/refreshtoken", bytes.NewReader(payload))
	if err != nil {
		return TokenPair{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return TokenPair{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return TokenPair{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed struct {
		Code int       `json:"code"`
		Data TokenPair `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return TokenPair{}, fmt.Errorf("decode refresh: %w", err)
	}
	if parsed.Code != 0 {
		return TokenPair{}, &APIError{Status: resp.StatusCode, Code: parsed.Code, Body: string(raw)}
	}
	return parsed.Data, nil
}

// Event is one decoded SSE frame from the conversation endpoint.
type Event struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	Timestamp int64           `json:"timestamp"`
}

// Conversation posts a chat body and streams decoded SSE events to onEvent.
func (c *Client) Conversation(ctx context.Context, token string, body map[string]interface{}, onEvent func(Event) error) error {
	base := c.nextBase()
	endpoint := base + "/api/v2/chat/conversation"

	iSign, err := c.signer.Generate(endpoint, body)
	if err != nil {
		return fmt.Errorf("generate i-sign: %w", err)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("i-sign", iSign)
	for k, v := range c.commonHeaders() {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		apiErr := &APIError{Status: resp.StatusCode, Body: string(raw)}
		// Try to decode an application level error frame.
		var frame Event
		if json.Unmarshal(raw, &frame) == nil && frame.Code != 0 {
			apiErr.Code = frame.Code
			apiErr.Message = frame.Message
		}
		return apiErr
	}

	return scanSSE(ctx, resp.Body, onEvent)
}

func scanSSE(ctx context.Context, r io.Reader, onEvent func(Event) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Not JSON, skip malformed frame.
			return nil
		}
		return onEvent(ev)
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		// id:, event:, retry:, comments are ignored.
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
