package server

import (
	"path/filepath"
	"testing"
)

func TestCodexAuthDirOverrideIsPersistentAndNonBrowsable(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	auth := filepath.Join(root, "private-auth")
	t.Setenv("ARTEX_CODEX_AUTH_DIR", auth)
	got, err := codexAuthPath(filepath.Join(root, "image"), data)
	if err != nil || got != filepath.Join(auth, "codex-oauth.json") {
		t.Fatalf("path=%q err=%v", got, err)
	}
	t.Setenv("ARTEX_CODEX_AUTH_DIR", filepath.Join(data, "private-auth"))
	if _, err := codexAuthPath(filepath.Join(root, "image"), data); err == nil {
		t.Fatal("browsable auth override accepted")
	}
}
