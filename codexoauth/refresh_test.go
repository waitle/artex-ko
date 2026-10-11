package codexoauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshRevocationDisconnectsWithoutEchoingSecrets(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"refresh_token_reused","secret":"fixture-refresh"}`)
	}))
	defer s.Close()
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	a.authOrigin = s.URL
	a.client = s.Client()
	a.save(tokens{Access: jwt(), Refresh: "fixture-refresh", Account: "a", Expiry: time.Now().Add(-time.Minute)})
	_, _, e := a.Credentials(context.Background())
	if e == nil || strings.Contains(e.Error(), "fixture-refresh") {
		t.Fatalf("unsafe error: %v", e)
	}
	if a.Status().Connected {
		t.Fatal("revoked account still connected")
	}
}
func TestConcurrentRefreshPersistsRotationExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated","expires_in":3600}`, jwt())
	}))
	defer s.Close()
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	a.authOrigin = s.URL
	a.client = s.Client()
	a.save(tokens{Access: jwt(), Refresh: "old", Account: "a", Expiry: time.Now().Add(-time.Minute)})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, e := a.Credentials(context.Background()); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls %d", calls.Load())
	}
	stored, e := a.load()
	if e != nil || stored.Refresh != "rotated" {
		t.Fatalf("rotation not persisted: %v", e)
	}
}
