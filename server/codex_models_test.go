package server

import (
	"github.com/Autumn-27/artex/codexoauth"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexModelCatalogRequiresOAuthNotAPIKey(t *testing.T) {
	s := &Server{codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))}
	w := httptest.NewRecorder()
	s.pgListModels(w, httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader(`{"provider":"openai-codex"}`)))
	if strings.Contains(w.Body.String(), "API Key") || !strings.Contains(w.Body.String(), "Codex") {
		t.Fatalf("wrong auth requirement: %s", w.Body)
	}
}
func TestCodexModelCatalogRejectsOverrides(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.pgListModels(w, httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader(`{"provider":"openai-codex","base_url":"https://evil.example","api_key":"fake"}`)))
	if w.Code != 400 {
		t.Fatalf("override accepted: %d %s", w.Code, w.Body)
	}
}
