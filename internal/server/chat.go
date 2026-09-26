package server

import (
	"context"
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

type chatDelta struct {
	Type    string `json:"type"`
	Content string `json:"content"`
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

func lastUserText(msgs []chatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			if t := strings.TrimSpace(extractText(msgs[i].Content)); t != "" {
				return t
			}
		}
	}
	return ""
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

// modelChain resolves the requested model and expands its fallback chain
// breadth first (depth <= 3, length <= 8, de-duplicated).
func (s *Server) modelChain(model string) []string {
	primary := s.resolveModel(model)
	cfg := s.cfg.Get()

	chain := []string{primary}
	seen := map[string]bool{primary: true}
	queue := []string{primary}

	for depth := 0; depth < 3; depth++ {
		if len(queue) == 0 || len(chain) >= 8 {
			break
		}
		var next []string
		for _, m := range queue {
			for _, f := range cfg.ModelFallback[m] {
				if f == "" || seen[f] {
					continue
				}
				seen[f] = true
				chain = append(chain, f)
				next = append(next, f)
				if len(chain) >= 8 {
					break
				}
			}
			if len(chain) >= 8 {
				break
			}
		}
		queue = next
	}
	return chain
}

func (s *Server) conversationBody(model, prompt string) map[string]interface{} {
	return map[string]interface{}{
		"model":              model,
		"prompt":             prompt,
		"chatResources":      []interface{}{},
		"imageOptions":       map[string]interface{}{},
		"webAccess":          "close",
		"deepResearchEnable": false,
		"userParameters":     map[string]interface{}{},
		"videos":             []interface{}{},
		"audios":             []interface{}{},
		"timezone":           "Asia/Shanghai",
	}
}

func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

// runChat walks the model fallback chain and, for each model, the account pool.
// Text deltas are forwarded to onDelta (may be nil). It returns the full text
// and the model that ultimately served the request.
func (s *Server) runChat(ctx context.Context, model string, messages []chatMessage, onDelta func(string) error) (string, string, error) {
	prompt := buildPrompt(messages)
	chain := s.modelChain(model)
	attempts := s.maxAttempts()

	var full strings.Builder
	var lastErr error
	usedModel := chain[0]

	for _, m := range chain {
		usedModel = m
		body := s.conversationBody(m, prompt)
		wroteAny := false

		for i := 0; i < attempts; i++ {
			acc, err := s.pool.Pick()
			if err != nil {
				lastErr = err
				break
			}
			wroteAny = false
			err = s.upstream.Conversation(ctx, acc.Token, body, func(ev upstream.Event) error {
				if ev.Code == 202 {
					var d chatDelta
					if json.Unmarshal(ev.Data, &d) == nil && d.Type == "chat" && d.Content != "" {
						full.WriteString(d.Content)
						wroteAny = true
						if onDelta != nil {
							if derr := onDelta(d.Content); derr != nil {
								return derr
							}
						}
					}
				}
				if ev.Code == 1002 || ev.Code == 1003 || ev.Code == 2002 || ev.Code == 3004 {
					return fmt.Errorf("deepsider code %d: %s", ev.Code, ev.Message)
				}
				return nil
			})
			if err == nil {
				s.pool.MarkSuccess(acc.ID)
				return full.String(), usedModel, nil
			}
			lastErr = err
			if wroteAny {
				// Response already partially delivered; cannot switch.
				return full.String(), usedModel, err
			}
			s.pool.MarkFailure(acc.ID, err.Error())
			s.stats.Log("warn", fmt.Sprintf("model=%s retry: %v", m, err))
		}
	}
	return full.String(), usedModel, lastErr
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
	if req.Stream {
		s.streamChat(w, r, req)
		return
	}
	s.collectChat(w, r, req)
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req chatRequest) {
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
	reportModel := s.resolveModel(req.Model)

	text, usedModel, err := s.runChat(r.Context(), req.Model, req.Messages, func(delta string) error {
		s.writeChunk(w, id, created, reportModel, delta, false)
		flusher.Flush()
		return nil
	})
	if usedModel != "" {
		reportModel = usedModel
	}

	if err != nil && text == "" {
		s.stats.Request(reportModel, false, 0)
		s.stats.Log("error", "chat failed: "+err.Error())
		s.writeChunk(w, id, created, reportModel, "[error] "+err.Error(), true)
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		return
	}

	s.writeChunk(w, id, created, reportModel, "", true)
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	s.stats.Request(reportModel, true, int64(len(text)))
	s.stats.Log("info", fmt.Sprintf("chat ok model=%s chars=%d", reportModel, len(text)))
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

func (s *Server) collectChat(w http.ResponseWriter, r *http.Request, req chatRequest) {
	text, usedModel, err := s.runChat(r.Context(), req.Model, req.Messages, nil)
	if err != nil && text == "" {
		s.stats.Request(usedModel, false, 0)
		s.stats.Log("error", "chat failed: "+err.Error())
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	resp := map[string]interface{}{
		"id":      randomID("chatcmpl-"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   usedModel,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": text},
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
	s.stats.Request(usedModel, true, int64(len(text)))
	s.stats.Log("info", fmt.Sprintf("chat ok model=%s chars=%d", usedModel, len(text)))
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
