package codexoauth

import (
	"context"
	"github.com/Autumn-27/norma/llm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestLogoutCancelsInFlightOAuthRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	a.save(tokens{Access: jwt(), Refresh: "r", Account: "a", Expiry: time.Now().Add(time.Hour)})
	p, e := newProvider(a, llm.Config{Model: "fixture", MaxRetries: -1}, s.URL, s.Client().Transport)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, e := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
		done <- e
	}()
	<-entered
	if e = a.Logout(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("successful response after logout")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("logout left OAuth request running")
	}
}
