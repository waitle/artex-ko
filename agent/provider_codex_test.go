package agent

import (
	"github.com/Autumn-27/artex/codexoauth"
	"testing"
)

func TestCodexConfigRejectsCallerCredentials(t *testing.T) {
	c := ConfigFrom("openai-codex", "fixture", "https://evil.example", "secret", "")
	if c.Provider() != "openai-codex" {
		t.Fatalf("provider=%s", c.Provider())
	}
	c.Codex = codexoauth.New(t.TempDir() + "/auth.json")
	if _, e := c.NewProvider(); e == nil {
		t.Fatal("caller credentials accepted")
	}
}
func TestCodexKeylessRequiresOwnedLogin(t *testing.T) {
	c := ConfigFrom("openai-codex", "fixture", "", "", "")
	if c.Ready() {
		t.Fatal("ready without login")
	}
	if _, e := c.NewProvider(); e == nil {
		t.Fatal("provider accepted missing auth owner")
	}
}
