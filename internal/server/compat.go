package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Anthropic Messages API (/v1/messages)
// ---------------------------------------------------------------------------

type anthropicRequest struct {
	Model     string          `json:"model"`
	System    json.RawMessage `json:"system"`
	Messages  []anthropicMsg  `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if len(req.System) > 0 {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: m.Content})
	}
	if len(msgs) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "messages is required")
		return
	}
	model := req.Model
	if model == "" {
		model = "auto"
	}
	id := randomID("msg_")

	if req.Stream {
		s.streamMessages(w, r, id, model, msgs)
		return
	}

	text, used, err := s.runChat(r.Context(), model, msgs, nil)
	if err != nil && text == "" {
		s.stats.Request(used, false, 0)
		writeAnthropicError(w, http.StatusBadGateway, err.Error())
		return
	}
	resp := map[string]interface{}{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         used,
		"content":       []map[string]interface{}{{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	s.stats.Request(used, true, int64(len(text)))
	caller := callerFrom(r.Context())
	n := s.stats.IncCaller(caller.Name)
	s.stats.Log("info", fmt.Sprintf("key=%s #%d proto=anthropic model=%s in=%q out=%q", caller.Name, n, used, truncateRunes(buildPrompt(msgs), 150), truncateRunes(text, 150)))
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, id, model string, msgs []chatMessage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	emit := func(event string, data map[string]interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	emit("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": id, "type": "message", "role": "assistant", "model": model,
			"content": []interface{}{}, "stop_reason": nil,
			"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	})
	emit("content_block_start", map[string]interface{}{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]interface{}{"type": "text", "text": ""},
	})
	emit("ping", map[string]interface{}{"type": "ping"})

	text, _, err := s.runChat(r.Context(), model, msgs, func(delta string) error {
		emit("content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]interface{}{"type": "text_delta", "text": delta},
		})
		return nil
	})
	_ = err
	_ = text

	emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": 0},
	})
	emit("message_stop", map[string]interface{}{"type": "message_stop"})
}

func writeAnthropicError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type":  "error",
		"error": map[string]interface{}{"type": "api_error", "message": message},
	})
}

// ---------------------------------------------------------------------------
// OpenAI Responses API (/v1/responses)
// ---------------------------------------------------------------------------

type responsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    json.RawMessage `json:"instructions"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Stream          bool            `json:"stream"`
}

func instructionsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		out := ""
		for _, p := range parts {
			out += p.Text
		}
		return out
	}
	return ""
}

func responsesInputToMessages(input json.RawMessage) []chatMessage {
	if len(input) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		b, _ := json.Marshal(s)
		return []chatMessage{{Role: "user", Content: b}}
	}
	var msgs []chatMessage
	if json.Unmarshal(input, &msgs) == nil {
		return msgs
	}
	return nil
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	var req responsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	msgs := make([]chatMessage, 0, 4)
	if instr := instructionsText(req.Instructions); instr != "" {
		b, _ := json.Marshal(instr)
		msgs = append(msgs, chatMessage{Role: "system", Content: b})
	}
	msgs = append(msgs, responsesInputToMessages(req.Input)...)
	if len(msgs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "input is required")
		return
	}
	model := req.Model
	if model == "" {
		model = "auto"
	}
	respID := randomID("resp_")

	if req.Stream {
		s.streamResponses(w, r, respID, model, msgs)
		return
	}

	text, used, err := s.runChat(r.Context(), model, msgs, nil)
	if err != nil && text == "" {
		s.stats.Request(used, false, 0)
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	resp := map[string]interface{}{
		"id":         respID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      used,
		"status":     "completed",
		"output": []map[string]interface{}{
			{
				"type":   "message",
				"id":     randomID("msg_"),
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": text, "annotations": []interface{}{}},
				},
			},
		},
		"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	s.stats.Request(used, true, int64(len(text)))
	caller := callerFrom(r.Context())
	n := s.stats.IncCaller(caller.Name)
	s.stats.Log("info", fmt.Sprintf("key=%s #%d proto=responses model=%s in=%q out=%q", caller.Name, n, used, truncateRunes(buildPrompt(msgs), 150), truncateRunes(text, 150)))
}

func (s *Server) streamResponses(w http.ResponseWriter, r *http.Request, respID, model string, msgs []chatMessage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	itemID := randomID("msg_")
	emit := func(event string, data map[string]interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	emit("response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id": respID, "object": "response", "created_at": time.Now().Unix(),
			"model": model, "status": "in_progress", "output": []interface{}{},
		},
	})
	emit("response.output_item.added", map[string]interface{}{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]interface{}{
			"type": "message", "id": itemID, "role": "assistant",
			"status": "in_progress", "content": []interface{}{},
		},
	})
	emit("response.content_part.added", map[string]interface{}{
		"type": "response.content_part.added", "item_id": itemID, "output_index": 0, "content_index": 0,
		"part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}},
	})

	text, _, err := s.runChat(r.Context(), model, msgs, func(delta string) error {
		emit("response.output_text.delta", map[string]interface{}{
			"type": "response.output_text.delta", "item_id": itemID,
			"output_index": 0, "content_index": 0, "delta": delta,
		})
		return nil
	})
	_ = err

	emit("response.output_text.done", map[string]interface{}{
		"type": "response.output_text.done", "item_id": itemID,
		"output_index": 0, "content_index": 0, "text": text,
	})
	emit("response.content_part.done", map[string]interface{}{
		"type": "response.content_part.done", "item_id": itemID, "output_index": 0, "content_index": 0,
		"part": map[string]interface{}{"type": "output_text", "text": text, "annotations": []interface{}{}},
	})
	emit("response.output_item.done", map[string]interface{}{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]interface{}{
			"type": "message", "id": itemID, "role": "assistant", "status": "completed",
			"content": []map[string]interface{}{{"type": "output_text", "text": text, "annotations": []interface{}{}}},
		},
	})
	emit("response.completed", map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"id": respID, "object": "response", "created_at": time.Now().Unix(),
			"model": model, "status": "completed",
			"output": []map[string]interface{}{
				{
					"type": "message", "id": itemID, "role": "assistant", "status": "completed",
					"content": []map[string]interface{}{{"type": "output_text", "text": text, "annotations": []interface{}{}}},
				},
			},
			"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
		},
	})
}
