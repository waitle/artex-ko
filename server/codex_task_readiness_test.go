package server

import (
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/codexoauth"
	"path/filepath"
	"testing"
)

func TestRevokedCodexTaskGlobalFallbackIsUnavailable(t *testing.T) {
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))
	s := &Server{llmOn: true, llmCfg: c, llmProv: &scriptedLLMProvider{}}
	if _, _, ok := s.globalProvider(); ok {
		t.Fatal("revoked task fallback ready")
	}
}
