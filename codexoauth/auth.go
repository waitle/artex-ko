// Package codexoauth owns one server-local ChatGPT account. It never imports CLI credentials.
package codexoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Backend = "https://chatgpt.com/backend-api/codex"
const clientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var ErrDisconnected = errors.New("Codex OAuth login required")

type rejected struct{ status int }

func (e rejected) Error() string {
	return fmt.Sprintf("Codex OAuth request rejected (HTTP %d)", e.status)
}

type Status struct {
	Connected bool      `json:"connected"`
	Pending   bool      `json:"pending,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}
type Start struct {
	VerificationURL string    `json:"verification_url"`
	UserCode        string    `json:"user_code"`
	ExpiresAt       time.Time `json:"expires_at"`
	PollInterval    int       `json:"poll_interval"`
}
type tokens struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token"`
	ID      string    `json:"id_token,omitempty"`
	Expires int64     `json:"expires_in,omitempty"`
	Expiry  time.Time `json:"expiry"`
	Account string    `json:"account"`
}
type pending struct {
	ID string
	Start
	Next time.Time
}
type Auth struct {
	epoch      context.Context
	revoke     context.CancelFunc
	mu         sync.Mutex
	path       string
	authOrigin string
	client     *http.Client
	pending    *pending
}

func New(path string) *Auth {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	epoch, revoke := context.WithCancel(context.Background())
	return &Auth{epoch: epoch, revoke: revoke, path: path, authOrigin: "https://auth.openai.com", client: &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (a *Auth) request(ctx context.Context, path, kind string, body io.Reader, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", a.authOrigin+path, body)
	if err != nil {
		return 0, errors.New("Codex OAuth request invalid")
	}
	req.Header.Set("Content-Type", kind)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("Codex OAuth service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return resp.StatusCode, rejected{resp.StatusCode}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return 0, errors.New("Codex OAuth response invalid")
	}
	return 200, nil
}
func (a *Auth) Start(ctx context.Context) (Start, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var r struct {
		ID       string          `json:"device_auth_id"`
		Code     string          `json:"user_code"`
		Alias    string          `json:"usercode"`
		Interval json.RawMessage `json:"interval"`
	}
	_, err := a.request(ctx, "/api/accounts/deviceauth/usercode", "application/json", strings.NewReader(`{"client_id":"`+clientID+`"}`), &r)
	if err != nil {
		return Start{}, err
	}
	if r.Code == "" {
		r.Code = r.Alias
	}
	if r.ID == "" || r.Code == "" {
		return Start{}, errors.New("Codex device response invalid")
	}
	n, _ := strconv.Atoi(strings.Trim(string(r.Interval), `"`))
	if n < 1 {
		n = 5
	}
	if n > 900 {
		n = 900
	}
	s := Start{"https://auth.openai.com/codex/device", r.Code, time.Now().Add(15 * time.Minute), n}
	a.pending = &pending{ID: r.ID, Start: s}
	return s, nil
}
func (a *Auth) Poll(ctx context.Context) (Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending
	if p == nil {
		return a.statusLocked(), nil
	}
	if time.Now().After(p.ExpiresAt) {
		a.pending = nil
		return Status{}, errors.New("Codex device login expired")
	}
	if time.Now().Before(p.Next) {
		return Status{Pending: true}, nil
	}
	p.Next = time.Now().Add(time.Duration(p.PollInterval) * time.Second)
	body, _ := json.Marshal(map[string]string{"device_auth_id": p.ID, "user_code": p.UserCode})
	var r struct {
		Code     string `json:"authorization_code"`
		Verifier string `json:"code_verifier"`
	}
	code, err := a.request(ctx, "/api/accounts/deviceauth/token", "application/json", strings.NewReader(string(body)), &r)
	if code == 403 || code == 404 {
		return Status{Pending: true}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if r.Code == "" || r.Verifier == "" {
		return Status{}, errors.New("Codex device authorization invalid")
	}
	t, err := a.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {r.Code}, "code_verifier": {r.Verifier}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}}, tokens{})
	if err != nil {
		return Status{}, err
	}
	if err = a.save(t); err != nil {
		return Status{}, err
	}
	a.pending = nil
	return a.statusLocked(), nil
}
func claims(token string) (string, time.Time) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", time.Time{}
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "", time.Time{}
	}
	var c struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(b, &c) != nil {
		return "", time.Time{}
	}
	return c.Auth.Account, time.Unix(c.Exp, 0)
}
func (a *Auth) exchange(ctx context.Context, v url.Values, old tokens) (tokens, error) {
	v.Set("client_id", clientID)
	var t tokens
	_, err := a.request(ctx, "/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(v.Encode()), &t)
	if err != nil {
		return t, err
	}
	t.Account, t.Expiry = claims(t.Access)
	if t.Account == "" {
		t.Account, _ = claims(t.ID)
	}
	if t.Expiry.IsZero() || t.Expiry.Unix() <= 0 {
		t.Expiry = time.Now().Add(time.Duration(t.Expires) * time.Second)
	}
	if t.Refresh == "" {
		t.Refresh = old.Refresh
	}
	if t.Access == "" || t.Refresh == "" || t.Account == "" || !t.Expiry.After(time.Now()) {
		return tokens{}, errors.New("Codex OAuth credentials invalid")
	}
	return t, nil
}
func (a *Auth) load() (tokens, error) {
	var t tokens
	info, e := os.Lstat(a.path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return t, ErrDisconnected
	}
	b, e := os.ReadFile(a.path)
	if e != nil || json.Unmarshal(b, &t) != nil || t.Access == "" || t.Refresh == "" || t.Account == "" {
		return tokens{}, ErrDisconnected
	}
	return t, nil
}
func (a *Auth) save(t tokens) error {
	b, _ := json.Marshal(t)
	f, e := os.CreateTemp(filepath.Dir(a.path), ".codex-oauth-*")
	if e != nil {
		return errors.New("Codex credential store unavailable")
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(name, a.path)
	}
	if e != nil {
		return errors.New("Codex credential persistence failed")
	}
	return nil
}
func (a *Auth) statusLocked() Status {
	t, e := a.load()
	return Status{Connected: e == nil && (t.Expiry.After(time.Now()) || t.Refresh != ""), ExpiresAt: t.Expiry}
}
func (a *Auth) Status() Status { a.mu.Lock(); defer a.mu.Unlock(); return a.statusLocked() }
func (a *Auth) Credentials(ctx context.Context) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.credentialsLocked(ctx)
}
func (a *Auth) beginRequest(ctx context.Context) (context.Context, string, string, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	token, account, e := a.credentialsLocked(ctx)
	if e != nil {
		return nil, "", "", nil, e
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.epoch, cancel)
	return requestCtx, token, account, func() { stop(); cancel() }, nil
}
func (a *Auth) credentialsLocked(ctx context.Context) (string, string, error) {
	t, e := a.load()
	if e != nil {
		return "", "", e
	}
	if time.Now().Add(time.Minute).After(t.Expiry) {
		t, e = a.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}}, t)
		if e != nil {
			var refusal rejected
			if errors.As(e, &refusal) && (refusal.status == 400 || refusal.status == 401 || refusal.status == 403) {
				_ = os.Remove(a.path)
			}
			return "", "", e
		}
		if e = a.save(t); e != nil {
			return "", "", e
		}
	}
	return t.Access, t.Account, nil
}
func (a *Auth) Logout() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoke()
	a.epoch, a.revoke = context.WithCancel(context.Background())
	a.pending = nil
	e := os.Remove(a.path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return errors.New("Codex logout failed")
	}
	return nil
}
