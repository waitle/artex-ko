package server

import (
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/codexoauth"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevokedCodexCachedProviderIsUnavailable(t *testing.T) {
	a := codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = a
	s := &Server{provByProfile: map[int64]*provEntry{1: {cfg: c}}}
	if _, _, ok := s.providerForProfile(1); ok {
		t.Fatal("revoked cached OAuth available")
	}
}
func TestRevokedCodexGlobalConfigReportsUnavailable(t *testing.T) {
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))
	s := &Server{llmOn: true, llmCfg: c}
	w := httptest.NewRecorder()
	s.getLLM(w, httptest.NewRequest(http.MethodGet, "/api/llm", nil))
	if !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("stale readiness: %s", w.Body)
	}
}
