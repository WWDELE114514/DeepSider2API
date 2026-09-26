package server

import (
	"log"
	"sync"
	"time"
)

// LogEntry is a single dashboard log line.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Stats aggregates request counters and a bounded log ring for the panel.
type Stats struct {
	mu              sync.Mutex
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	TotalTokens     int64
	PerModel        map[string]int64
	PerCaller       map[string]int64
	Recent          []LogEntry
	maxLogs         int
}

// NewStats creates an empty stats collector.
func NewStats() *Stats {
	return &Stats{PerModel: map[string]int64{}, PerCaller: map[string]int64{}, maxLogs: 500}
}

// IncCaller increments and returns the call count for a caller (api key name).
func (s *Stats) IncCaller(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		name = "?"
	}
	s.PerCaller[name]++
	return s.PerCaller[name]
}

// Log appends a log entry to the ring buffer and also prints it to the console.
func (s *Stats) Log(level, message string) {
	s.mu.Lock()
	s.Recent = append(s.Recent, LogEntry{Time: time.Now(), Level: level, Message: message})
	if len(s.Recent) > s.maxLogs {
		s.Recent = s.Recent[len(s.Recent)-s.maxLogs:]
	}
	s.mu.Unlock()
	log.Printf("[%s] %s", level, message)
}

// Request records the outcome of a chat request.
func (s *Stats) Request(model string, success bool, tokens int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalRequests++
	if success {
		s.SuccessRequests++
	} else {
		s.FailedRequests++
	}
	s.TotalTokens += tokens
	if model != "" {
		s.PerModel[model]++
	}
}

// Snapshot returns a copy of the counters and logs for JSON encoding.
func (s *Stats) Snapshot() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	perModel := make(map[string]int64, len(s.PerModel))
	for k, v := range s.PerModel {
		perModel[k] = v
	}
	perCaller := make(map[string]int64, len(s.PerCaller))
	for k, v := range s.PerCaller {
		perCaller[k] = v
	}
	logs := make([]LogEntry, len(s.Recent))
	copy(logs, s.Recent)
	return map[string]interface{}{
		"total_requests":   s.TotalRequests,
		"success_requests": s.SuccessRequests,
		"failed_requests":  s.FailedRequests,
		"total_tokens":     s.TotalTokens,
		"per_model":        perModel,
		"per_caller":       perCaller,
		"logs":             logs,
	}
}

// Logs returns a copy of the recent log ring.
func (s *Stats) Logs() []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogEntry, len(s.Recent))
	copy(out, s.Recent)
	return out
}

// ClearLogs empties the log ring.
func (s *Stats) ClearLogs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Recent = nil
}
