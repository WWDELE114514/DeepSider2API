package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) imageBody(model, prompt, resolution, ratio string) map[string]interface{} {
	return map[string]interface{}{
		"model":              model,
		"prompt":             prompt,
		"imageOptions":       map[string]interface{}{"resolution": resolution, "ratio": ratio},
		"chatResources":      []interface{}{},
		"webAccess":          "close",
		"deepResearchEnable": false,
		"userParameters":     map[string]interface{}{},
		"videos":             []interface{}{},
		"audios":             []interface{}{},
		"timezone":           "Asia/Shanghai",
	}
}

// mapSize converts an OpenAI size (or explicit resolution/ratio) into
// DeepSider imageOptions.
func mapSize(size, resolution, ratio string) (string, string) {
	if resolution == "" {
		resolution = "1k"
	}
	if ratio == "" {
		ratio = "1:1"
	}
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024", "512x512", "256x256":
		ratio = "1:1"
	case "1792x1024", "1536x1024", "1344x768":
		ratio = "16:9"
	case "1024x1792", "1024x1536", "768x1344":
		ratio = "9:16"
	case "1024x768":
		ratio = "4:3"
	case "768x1024":
		ratio = "3:4"
	}
	if strings.Contains(size, "2048") {
		resolution = "2k"
	}
	if strings.Contains(size, "4096") {
		resolution = "4k"
	}
	return resolution, ratio
}

func (s *Server) resolveImageModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.Contains(model, "/") {
		return model
	}
	if def := s.cfg.Get().Image.DefaultModel; def != "" {
		return def
	}
	return "openai/gpt-image-2"
}

// generateImages runs the model chain over the account pool and returns the
// raw image URLs reported by DeepSider.
func (s *Server) generateImages(ctx context.Context, model, prompt, resolution, ratio string) ([]string, error) {
	body := s.imageBody(model, prompt, resolution, ratio)
	attempts := s.maxAttempts()
	var lastErr error

	for i := 0; i < attempts; i++ {
		acc, err := s.pool.Pick()
		if err != nil {
			lastErr = err
			break
		}
		result, err := s.upstream.GenerateImage(ctx, acc.Token, body)
		if err == nil && len(result.URLs) > 0 {
			s.pool.MarkSuccess(acc.ID)
			return result.URLs, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no image url returned (model may have been blocked by safety filter)")
		}
		s.pool.MarkFailure(acc.ID, lastErr.Error())
		s.stats.Log("warn", "image retry: "+lastErr.Error())
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("image generation failed")
	}
	return nil, lastErr
}

// materializeImages optionally transloads images to local storage (so they
// survive DeepSider's 24h expiry) and builds url/download_url pairs.
func (s *Server) materializeImages(ctx context.Context, r *http.Request, urls []string) []map[string]interface{} {
	cfg := s.cfg.Get()
	base := s.publicBase(r)
	out := make([]map[string]interface{}, 0, len(urls))

	for _, u := range urls {
		item := map[string]interface{}{"url": u}
		if cfg.Image.Persist {
			if name, err := s.transload(ctx, u); err == nil {
				link := base + "/files/" + name
				item["url"] = link
				item["download_url"] = link + "?download=1"
			} else {
				s.stats.Log("warn", "transload failed: "+err.Error())
			}
		}
		if _, ok := item["download_url"]; !ok {
			item["download_url"] = item["url"]
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) publicBase(r *http.Request) string {
	cfg := s.cfg.Get()
	if cfg.PublicBaseURL != "" {
		return strings.TrimRight(cfg.PublicBaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if cfg.TrustProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
	}
	return scheme + "://" + host
}

func extFromURL(u string) string {
	p := u
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	switch ext := strings.ToLower(filepath.Ext(p)); ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return ext
	}
	return ".png"
}

func (s *Server) transload(ctx context.Context, rawURL string) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:12]) + extFromURL(rawURL)

	if err := os.MkdirAll(s.filesDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.filesDir, name), data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.filesDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeFile(w, r, path)
}

func (s *Server) cleanFiles() {
	ttl := time.Duration(s.cfg.Get().Image.PersistTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	entries, err := os.ReadDir(s.filesDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ttl)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.filesDir, e.Name()))
		}
	}
}

// handleImageGenerations implements the OpenAI /v1/images/generations endpoint.
func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		Size           string `json:"size"`
		N              int    `json:"n"`
		Resolution     string `json:"resolution"`
		Ratio          string `json:"ratio"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	model := s.resolveImageModel(req.Model)
	resolution, ratio := mapSize(req.Size, req.Resolution, req.Ratio)

	urls, err := s.generateImages(r.Context(), model, req.Prompt, resolution, ratio)
	if err != nil {
		s.stats.Request("image:"+model, false, 0)
		caller := callerFrom(r.Context())
		n := s.stats.IncCaller(caller.Name)
		s.stats.Log("error", fmt.Sprintf("key=%s #%d model=%s in=%q err=%s", caller.Name, n, model, truncateRunes(req.Prompt, 150), err.Error()))
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	data := s.materializeImages(r.Context(), r, urls)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"created": time.Now().Unix(),
		"data":    data,
	})
	s.stats.Request("image:"+model, true, 0)
	caller := callerFrom(r.Context())
	n := s.stats.IncCaller(caller.Name)
	s.stats.Log("info", fmt.Sprintf("key=%s #%d model=%s in=%q images=%d", caller.Name, n, model, truncateRunes(req.Prompt, 150), len(urls)))
}
