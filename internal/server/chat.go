package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/upstream"
)

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

var shanghai = time.FixedZone("CST", 8*3600)

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func buildPrompt(msgs []chatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		text := strings.TrimSpace(extractText(m.Content))
		if text == "" {
			continue
		}
		switch m.Role {
		case "system":
			b.WriteString(text)
			b.WriteString("\n\n")
		case "assistant":
			b.WriteString("Assistant: ")
			b.WriteString(text)
			b.WriteString("\n")
		default:
			b.WriteString("User: ")
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) resolveModel(model string) string {
	cfg := s.cfg.Get()
	if !cfg.AutoModel.Enabled {
		return model
	}
	if model != cfg.AutoModel.VirtualID {
		return model
	}
	hour := time.Now().In(shanghai).Hour()
	day := hour >= cfg.AutoModel.DayStart && hour < cfg.AutoModel.DayEnd
	if day && cfg.AutoModel.DayPrimary != "" {
		return cfg.AutoModel.DayPrimary
	}
	if !day && cfg.AutoModel.NightPrimary != "" {
		return cfg.AutoModel.NightPrimary
	}
	return model
}

func (s *Server) conversationBody(model, prompt string) map[string]interface{} {
	return map[string]interface{}{
		"model":                model,
		"prompt":               prompt,
		"chatResources":        []interface{}{},
		"imageOptions":         map[string]interface{}{},
		"webAccess":            "close",
		"deepResearchEnable":   false,
		"userParameters":       map[string]interface{}{},
		"videos":               []interface{}{},
		"audios":               []interface{}{},
		"timezone":             "Asia/Shanghai",
	}
}

func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "messages is required")
		return
	}
	if req.Model == "" {
		req.Model = "auto"
	}

	prompt := buildPrompt(req.Messages)
	model := s.resolveModel(req.Model)
	body := s.conversationBody(model, prompt)

	if req.Stream {
		s.streamChat(w, r, req, model, body)
		return
	}
	s.collectChat(w, r, req, model, body)
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req chatRequest, model string, body map[string]interface{}) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	id := randomID("chatcmpl-")
	created := time.Now().Unix()

	var contentLen int
	var wroteAny bool
	var lastErr error

	attempts := s.maxAttempts()
	for i := 0; i < attempts; i++ {
		acc, err := s.pool.Pick()
		if err != nil {
			lastErr = err
			break
		}
		wroteAny = false
		err = s.upstream.Conversation(r.Context(), acc.Token, body, func(ev upstream.Event) error {
			if ev.Code == 202 {
				var d struct {
					Type    string `json:"type"`
					Content string `json:"content"`
				}
				if json.Unmarshal(ev.Data, &d) == nil && d.Type == "chat" && d.Content != "" {
					s.writeChunk(w, id, created, model, d.Content, false)
					contentLen += len(d.Content)
					wroteAny = true
					flusher.Flush()
				}
			}
			if ev.Code == 1002 || ev.Code == 1003 || ev.Code == 2002 || ev.Code == 3004 {
				return fmt.Errorf("deepsider code %d: %s", ev.Code, ev.Message)
			}
			return nil
		})
		if err == nil {
			s.pool.MarkSuccess(acc.ID)
			lastErr = nil
			break
		}
		lastErr = err
		if wroteAny {
			break
		}
		s.pool.MarkFailure(acc.ID, err.Error())
		s.stats.Log("warn", "stream retry: "+err.Error())
	}

	if lastErr != nil && !wroteAny {
		s.stats.Request(model, false, 0)
		s.stats.Log("error", "chat failed: "+lastErr.Error())
		s.writeChunk(w, id, created, model, "[error] "+lastErr.Error(), true)
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		return
	}

	s.writeChunk(w, id, created, model, "", true)
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	s.stats.Request(model, true, int64(contentLen))
	s.stats.Log("info", fmt.Sprintf("chat ok model=%s chars=%d", model, contentLen))
}

func (s *Server) writeChunk(w http.ResponseWriter, id string, created int64, model, content string, done bool) {
	delta := map[string]interface{}{}
	if content != "" {
		delta["content"] = content
	}
	var finish interface{}
	if done {
		finish = "stop"
	}
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	b, _ := json.Marshal(chunk)
	w.Write([]byte("data: "))
	w.Write(b)
	w.Write([]byte("\n\n"))
}

func (s *Server) collectChat(w http.ResponseWriter, r *http.Request, req chatRequest, model string, body map[string]interface{}) {
	var content strings.Builder
	var lastErr error

	attempts := s.maxAttempts()
	for i := 0; i < attempts; i++ {
		acc, err := s.pool.Pick()
		if err != nil {
			lastErr = err
			break
		}
		content.Reset()
		err = s.upstream.Conversation(r.Context(), acc.Token, body, func(ev upstream.Event) error {
			if ev.Code == 202 {
				var d struct {
					Type    string `json:"type"`
					Content string `json:"content"`
				}
				if json.Unmarshal(ev.Data, &d) == nil && d.Type == "chat" {
					content.WriteString(d.Content)
				}
			}
			if ev.Code == 1002 || ev.Code == 1003 || ev.Code == 2002 || ev.Code == 3004 {
				return fmt.Errorf("deepsider code %d: %s", ev.Code, ev.Message)
			}
			return nil
		})
		if err == nil {
			s.pool.MarkSuccess(acc.ID)
			lastErr = nil
			break
		}
		lastErr = err
		s.pool.MarkFailure(acc.ID, err.Error())
		s.stats.Log("warn", "collect retry: "+err.Error())
	}

	if lastErr != nil {
		s.stats.Request(model, false, 0)
		s.stats.Log("error", "chat failed: "+lastErr.Error())
		writeOpenAIError(w, http.StatusBadGateway, lastErr.Error())
		return
	}

	resp := map[string]interface{}{
		"id":      randomID("chatcmpl-"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": content.String()},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	s.stats.Request(model, true, int64(content.Len()))
	s.stats.Log("info", fmt.Sprintf("chat ok model=%s chars=%d", model, content.Len()))
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "invalid_request_error",
			"code":    status,
		},
	})
}
