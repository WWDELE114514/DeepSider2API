package server

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// handleImageGenerations implements the OpenAI /v1/images/generations endpoint
// on top of DeepSider's conversation stream.
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
	body := s.imageBody(model, req.Prompt, resolution, ratio)

	attempts := s.maxAttempts()
	var lastErr error

	for i := 0; i < attempts; i++ {
		acc, err := s.pool.Pick()
		if err != nil {
			lastErr = err
			break
		}
		result, err := s.upstream.GenerateImage(r.Context(), acc.Token, body)
		if err == nil && len(result.URLs) > 0 {
			s.pool.MarkSuccess(acc.ID)
			data := make([]map[string]interface{}, 0, len(result.URLs))
			for _, u := range result.URLs {
				data = append(data, map[string]interface{}{"url": u})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"created": time.Now().Unix(),
				"data":    data,
			})
			s.stats.Request("image:"+model, true, 0)
			s.stats.Log("info", fmt.Sprintf("image ok model=%s n=%d", model, len(result.URLs)))
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no image url returned (model may have been blocked by safety filter)")
		}
		s.pool.MarkFailure(acc.ID, lastErr.Error())
		s.stats.Log("warn", "image retry: "+lastErr.Error())
	}

	msg := "image generation failed"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	s.stats.Request("image:"+model, false, 0)
	s.stats.Log("error", "image failed: "+msg)
	writeOpenAIError(w, http.StatusBadGateway, msg)
}
