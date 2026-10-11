package codexoauth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func jwt() string {
	return "x." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"account"}}`, time.Now().Add(time.Hour).Unix()))) + ".x"
}
func TestDeviceLoginPersistsPrivateCredentialsAndLogout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"device","user_code":"ABCD-EFGHI","interval":"1"}`)
		case "/api/accounts/deviceauth/token":
			fmt.Fprint(w, `{"authorization_code":"code","code_verifier":"verifier"}`)
		case "/oauth/token":
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh","expires_in":3600}`, jwt())
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	a := New(path)
	a.authOrigin = s.URL
	a.client = s.Client()
	start, err := a.Start(context.Background())
	if err != nil || start.UserCode != "ABCD-EFGHI" {
		t.Fatalf("start: %+v %v", start, err)
	}
	status, err := a.Poll(context.Background())
	if err != nil || !status.Connected {
		t.Fatalf("poll: %+v %v", status, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private store: %v %v", info, err)
	}
	if err := a.Logout(); err != nil {
		t.Fatal(err)
	}
	if a.Status().Connected {
		t.Fatal("still connected")
	}
	if _, _, err := a.Credentials(context.Background()); err == nil {
		t.Fatal("revoked credential accepted")
	}
}
