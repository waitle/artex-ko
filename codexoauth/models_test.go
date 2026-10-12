package codexoauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fixtureTransport struct {
	base http.RoundTripper
	url  string
	t    *testing.T
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != Backend+"/models?client_version=0.162.1" {
		f.t.Errorf("unexpected catalog destination %s", r.URL)
	}
	r = r.Clone(r.Context())
	u, _ := url.Parse(f.url)
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	return f.base.RoundTrip(r)
}
func TestModelsAuthenticatedCatalogAliasesAndDeduplication(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("ChatGPT-Account-ID") != "account" {
			t.Error("missing OAuth headers")
		}
		fmt.Fprint(w, `{"models":[{"slug":"a"},{"name":"b"},"a",{"id":"c"}]}`)
	}))
	defer s.Close()
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	a.save(tokens{Access: jwt(), Refresh: "r", Account: "account", Expiry: time.Now().Add(time.Hour)})
	a.client.Transport = fixtureTransport{s.Client().Transport, s.URL, t}
	got, e := a.Models(context.Background())
	if e != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("catalog %v %v", got, e)
	}
}
