package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelRoutes(t *testing.T) {
	s := newTestServer(t, `{"api_key":"k"}`)
	s.mux = http.NewServeMux()
	s.routes()

	// /panel/ serves the panel HTML.
	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/panel/ status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DeepSider2API") || !strings.Contains(body, "backdrop-filter") {
		t.Fatalf("/panel/ does not look like the glass panel")
	}

	// /panel redirects to /panel/.
	req = httptest.NewRequest(http.MethodGet, "/panel", nil)
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/panel status = %d, want 301", rec.Code)
	}

	// / redirects into the panel.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("/ status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/panel/" {
		t.Fatalf("/ Location = %q, want /panel/", loc)
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t, `{"api_key":"k"}`)
	s.mux = http.NewServeMux()
	s.routes()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}
