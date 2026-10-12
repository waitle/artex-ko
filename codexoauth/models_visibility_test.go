package codexoauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestModelsExcludesHiddenCatalogEntries(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"slug":"visible","visibility":"list"},{"slug":"reserve","visibility":"hide"},{"slug":"internal","visibility":" HIDDEN "},{"id":"also-visible"}]}`)
	}))
	defer s.Close()
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	if err := a.save(tokens{Access: jwt(), Refresh: "r", Account: "account", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a.client.Transport = fixtureTransport{s.Client().Transport, s.URL, t}
	got, err := a.Models(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"visible", "also-visible"}) {
		t.Fatalf("catalog=%v err=%v", got, err)
	}
}
