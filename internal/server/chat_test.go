package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
)

func newTestServer(t *testing.T, cfgJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return &Server{cfg: store}
}

func TestModelChain(t *testing.T) {
	s := newTestServer(t, `{
		"auto_model": {"enabled": false, "virtual_id": "auto"},
		"model_fallback": {
			"a": ["b", "c"],
			"b": ["c", "d"],
			"c": ["e"]
		}
	}`)

	got := s.modelChain("a")
	want := []string{"a", "b", "c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelChain = %v, want %v", got, want)
	}
}

func TestModelChainNoFallback(t *testing.T) {
	s := newTestServer(t, `{"auto_model": {"enabled": false}, "model_fallback": {}}`)
	got := s.modelChain("gpt-x")
	if !reflect.DeepEqual(got, []string{"gpt-x"}) {
		t.Fatalf("modelChain = %v, want [gpt-x]", got)
	}
}

func TestModelChainDepthLimit(t *testing.T) {
	s := newTestServer(t, `{
		"auto_model": {"enabled": false},
		"model_fallback": {"m0": ["m1"], "m1": ["m2"], "m2": ["m3"], "m3": ["m4"]}
	}`)
	got := s.modelChain("m0")
	if len(got) > 8 {
		t.Fatalf("chain too long: %v", got)
	}
	if got[0] != "m0" {
		t.Fatalf("chain must start with requested model, got %v", got)
	}
}
