package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCodexRoutesRemainAuthenticated(t *testing.T) {
	s := &Server{jwtKey: []byte("fixture-signing-key-fixture-signing-key")}
	h := s.Handler()
	for _, route := range []struct{ method, path string }{{"POST", "/api/llm/oauth/codex/start"}, {"POST", "/api/llm/oauth/codex/poll"}, {"GET", "/api/llm/oauth/codex/status"}, {"DELETE", "/api/llm/oauth/codex"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unguarded %s %s: %d", route.method, route.path, w.Code)
		}
	}
	token, e := signJWT(s.jwtKey)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/api/llm/oauth/codex/status", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("authenticated status %d", w.Code)
	}
}
