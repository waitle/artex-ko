package agent

import "testing"

func TestCodexEnvironmentDoesNotBecomeAPIKeyProvider(t *testing.T) {
	t.Setenv("ARTEX_LLM_PROVIDER", "openai-codex")
	t.Setenv("ARTEX_LLM_MODEL", "fixture")
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("ANTHROPIC_API_KEY", "")
	c, ok := FromEnv()
	if !ok || c.Provider() != "openai-codex" || c.APIKey != "" {
		t.Fatalf("unexpected config %s candidate %v", c.Provider(), ok)
	}
}
