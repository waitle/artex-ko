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

func TestCodexStatusAndLogoutContract(t *testing.T) {
	s := &Server{codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))}
	w := httptest.NewRecorder()
	s.codexStatus(w, httptest.NewRequest(http.MethodGet, "/api/llm/oauth/codex/status", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":false`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.codexLogout(w, httptest.NewRequest(http.MethodDelete, "/api/llm/oauth/codex", nil))
	if w.Code != 200 {
		t.Fatalf("logout %d %s", w.Code, w.Body)
	}
}
func TestCodexServerBindsOwnedAccount(t *testing.T) {
	s := &Server{codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))}
	c := s.bindOAuth(agent.ConfigFrom("openai-codex", "fixture", "", "", ""))
	if c.Codex != s.codex {
		t.Fatal("not bound")
	}
}
func TestCodexCredentialPathRejectsBrowsableLocation(t *testing.T) {
	d := t.TempDir()
	if _, e := codexAuthPath(d, d); e == nil {
		t.Fatal("browsable credentials accepted")
	}
	if _, e := codexAuthPath(filepath.Join(d, "data", "keys"), filepath.Join(d, "data")); e == nil {
		t.Fatal("nested credentials accepted")
	}
}
