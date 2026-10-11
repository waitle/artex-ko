package codexoauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshNeverFollowsCredentialRedirect(t *testing.T) {
	var leaks atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks.Add(1) }))
	defer sink.Close()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer issuer.Close()
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	a.authOrigin = issuer.URL
	a.save(tokens{Access: jwt(), Refresh: "fixture-secret", Account: "a", Expiry: time.Now().Add(-time.Minute)})
	if _, _, e := a.Credentials(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
	if leaks.Load() != 0 {
		t.Fatal("credential redirect followed")
	}
}
func TestCredentialTransportRejectsOtherOriginsBeforeNetwork(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	tr := transport{auth: a, origin: Backend, base: http.DefaultTransport}
	r, _ := http.NewRequestWithContext(context.Background(), "POST", "https://evil.example/responses", strings.NewReader(`{}`))
	if _, e := tr.RoundTrip(r); e == nil || !strings.Contains(e.Error(), "destination rejected") {
		t.Fatalf("unsafe destination accepted: %v", e)
	}
}
func TestAuthStoreRejectsInsecurePermissions(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	if e := a.save(tokens{Access: jwt(), Refresh: "r", Account: "a", Expiry: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(a.path, 0644); e != nil {
		t.Fatal(e)
	}
	if a.Status().Connected {
		t.Fatal("public credential file accepted")
	}
}
