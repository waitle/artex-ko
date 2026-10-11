package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/codexoauth"
	"github.com/Autumn-27/artex/db"
)

// This driver exercises the real db profile scan without any live database.
// A transaction would mutate the selected profile and is forbidden by this test.
type codexRuntimeSQL struct {
	begins      int
	activeCodex bool
}

func (d *codexRuntimeSQL) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *codexRuntimeSQL) Driver() driver.Driver                        { return d }
func (d *codexRuntimeSQL) Open(string) (driver.Conn, error)             { return d, nil }
func (d *codexRuntimeSQL) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (d *codexRuntimeSQL) Close() error { return nil }
func (d *codexRuntimeSQL) Begin() (driver.Tx, error) {
	d.begins++
	return nil, errors.New("activation must not write")
}
func (d *codexRuntimeSQL) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	r := &codexRuntimeRows{}
	if strings.Contains(q, "FROM llm_profiles") {
		if strings.Contains(q, "WHERE id=") || (d.activeCodex && strings.Contains(q, "WHERE is_default")) {
			r.values = []driver.Value{int64(2), "codex", "openai-codex", "", "", "fixture", "", float64(0), float64(0), int64(0), "", false, int64(0), false, "", true, int64(0), "", "", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)}
		}
	}
	return r, nil
}

type codexRuntimeRows struct{ values []driver.Value }

func (r *codexRuntimeRows) Columns() []string {
	cols := make([]string, 25)
	for i := range cols {
		cols[i] = "column"
	}
	return cols
}
func (r *codexRuntimeRows) Close() error { return nil }
func (r *codexRuntimeRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func writeCodexRuntimeCredentials(t *testing.T, path string, expiry time.Time) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "account": "fixture-account", "expiry": expiry})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// End-to-end validation of the readiness gates after the actual auth refresh
// path removes credentials on permanent rejection. All HTTP stays local.
func TestCodexRefreshRejectedCachedChatIsUnavailable(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected OAuth path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fixture.Close()
	transport := fixture.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com" // httptest fixture certificate
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	path := filepath.Join(t.TempDir(), "auth.json")
	auth := codexoauth.New(path)
	http.DefaultTransport = old
	writeCodexRuntimeCredentials(t, path, time.Now().Add(-time.Hour))
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = auth
	cached := &agent.ChatAgent{}
	s := &Server{m: &Manager{}, llmOn: true, llmCfg: c, chatAgent: cached, profChatAgents: map[int64]*agent.ChatAgent{2: cached}, provByProfile: map[int64]*provEntry{2: {cfg: c}}}
	if s.chatAgentRef() == nil || s.chatAgentForProfile(2) == nil {
		t.Fatal("refreshable account unexpectedly unavailable before rejection")
	}
	if _, _, err := auth.Credentials(context.Background()); err == nil {
		t.Fatal("fixture refresh rejection unexpectedly succeeded")
	}
	if auth.Status().Connected {
		t.Fatal("permanent rejection did not disconnect")
	}
	if s.chatAgentRef() != nil || s.chatAgentForProfile(2) != nil || s.resolveChatAgent(&db.Conversation{}) != nil {
		t.Fatal("permanent rejection left cached chat ready")
	}
}

func TestCodexUnavailableActiveReapplyDisablesOldGlobalModel(t *testing.T) {
	sqlDB := sql.OpenDB(&codexRuntimeSQL{activeCodex: true})
	defer sqlDB.Close()
	s := &Server{m: &Manager{pg: &db.DB{DB: sqlDB}}, codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json")), llmCfg: agent.ConfigFrom("openai", "old-model", "", "fixture-key", ""), llmOn: true, llmProf: "old", chatAgent: &agent.ChatAgent{}, llmProv: &scriptedLLMProvider{}, llmDirect: &scriptedLLMProvider{}}
	s.reapplyActiveProfile()
	if s.llmOn || s.chatAgentRef() != nil || s.llmProv != nil || s.llmDirect != nil {
		t.Fatal("unavailable selected profile kept old global model ready")
	}
	if s.llmCfg.Provider() != "openai-codex" || s.llmCfg.Model != "fixture" || s.llmProf != "codex" {
		t.Fatal("unavailable selected profile lost its identity")
	}
}

func TestCodexPollRestoresEnvironmentWithoutDatabase(t *testing.T) {
	t.Setenv("ARTEX_LLM_PROVIDER", "openai-codex")
	t.Setenv("ARTEX_LLM_MODEL", "env-model")
	t.Setenv("ARTEX_LLM_BASE_URL", "")
	t.Setenv("ARTEX_LLM_PROXY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	writeCodexRuntimeCredentials(t, path, time.Now().Add(time.Hour))
	s := &Server{m: &Manager{dir: t.TempDir()}, codex: codexoauth.New(path)}
	w := httptest.NewRecorder()
	s.codexPoll(w, httptest.NewRequest(http.MethodPost, "/api/llm/oauth/codex/poll", nil))
	if w.Code != 200 || !s.llmOn || s.chatAgentRef() == nil {
		t.Fatalf("no-DB env restore: %d %s", w.Code, w.Body)
	}
}

func TestCodexPollRestoresEnvironmentAfterLoginAndRelogin(t *testing.T) {
	t.Setenv("ARTEX_LLM_PROVIDER", "openai-codex")
	t.Setenv("ARTEX_LLM_MODEL", "env-model")
	t.Setenv("ARTEX_LLM_BASE_URL", "")
	t.Setenv("ARTEX_LLM_PROXY", "")
	sqlDB := sql.OpenDB(&codexRuntimeSQL{})
	defer sqlDB.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	s := &Server{m: &Manager{pg: &db.DB{DB: sqlDB}, dir: t.TempDir()}, codex: codexoauth.New(path)}
	cfg, _ := agent.FromEnv()
	if err := s.applyLLM(s.bindOAuth(cfg)); err == nil {
		t.Fatal("startup unexpectedly ready before login")
	}
	for i := 0; i < 2; i++ {
		writeCodexRuntimeCredentials(t, path, time.Now().Add(time.Hour))
		w := httptest.NewRecorder()
		s.codexPoll(w, httptest.NewRequest(http.MethodPost, "/api/llm/oauth/codex/poll", nil))
		if w.Code != 200 || !s.llmOn || s.llmCfg.Model != "env-model" || s.chatAgentRef() == nil {
			t.Fatalf("login %d failed to restore env runtime: code=%d on=%v model=%q", i, w.Code, s.llmOn, s.llmCfg.Model)
		}
		s.codexLogout(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/llm/oauth/codex", nil))
	}
}

func TestCodexUnavailablePinDoesNotUseOtherGlobalModel(t *testing.T) {
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))
	s := &Server{m: &Manager{}, llmOn: true, llmCfg: agent.ConfigFrom("openai", "other", "", "fixture-key", ""), chatAgent: &agent.ChatAgent{}, provByProfile: map[int64]*provEntry{2: {cfg: c}}}
	id := int64(2)
	if s.resolveChatAgent(&db.Conversation{LLMProfileID: &id}) != nil {
		t.Fatal("disconnected Codex selection used another global model")
	}
}

func TestCodexDisconnectedCachedChatIsUnavailable(t *testing.T) {
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	c.Codex = codexoauth.New(filepath.Join(t.TempDir(), "auth.json"))
	cached := &agent.ChatAgent{}
	s := &Server{m: &Manager{}, llmOn: true, llmCfg: c, chatAgent: cached, profChatAgents: map[int64]*agent.ChatAgent{2: cached}, provByProfile: map[int64]*provEntry{2: {cfg: c}}}
	if s.chatAgentRef() != nil {
		t.Fatal("disconnected global chat cache still ready")
	}
	if s.chatAgentForProfile(2) != nil {
		t.Fatal("disconnected profile chat cache still ready")
	}
	if s.resolveChatAgent(&db.Conversation{}) != nil {
		t.Fatal("disconnected conversation precheck still ready")
	}
}

func TestCodexLogoutClearsGlobalChatPrecheck(t *testing.T) {
	c := agent.ConfigFrom("openai-codex", "fixture", "", "", "")
	s := &Server{m: &Manager{}, codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json")), llmCfg: c, llmOn: true, llmProf: "codex", chatAgent: &agent.ChatAgent{}, llmProv: &scriptedLLMProvider{}, llmDirect: &scriptedLLMProvider{}}
	w := httptest.NewRecorder()
	s.codexLogout(w, httptest.NewRequest(http.MethodDelete, "/api/llm/oauth/codex", nil))
	if w.Code != 200 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if s.chatAgentRef() != nil || s.resolveChatAgent(&db.Conversation{}) != nil {
		t.Fatal("logout left global conversation precheck ready")
	}
	if s.llmOn || s.llmCfg != (agent.Config{}) || s.chatAgent != nil || s.llmProv != nil || s.llmDirect != nil || s.llmProf != "" {
		t.Fatal("logout retained Codex runtime state")
	}
}

func TestCodexDisconnectedActivationRetainsPreviousRuntime(t *testing.T) {
	mock := &codexRuntimeSQL{}
	sqlDB := sql.OpenDB(mock)
	defer sqlDB.Close()
	previous := agent.ConfigFrom("openai", "previous-model", "", "fixture-key", "")
	previousChat := &agent.ChatAgent{}
	s := &Server{m: &Manager{pg: &db.DB{DB: sqlDB}}, codex: codexoauth.New(filepath.Join(t.TempDir(), "auth.json")), llmCfg: previous, llmOn: true, llmProf: "previous", chatAgent: previousChat}
	w := httptest.NewRecorder()
	s.pgActivateProfile(w, httptest.NewRequest(http.MethodPost, "/api/llm/profiles/activate", strings.NewReader(`{"id":2}`)))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Codex OAuth login required") {
		t.Fatalf("activation = %d %s, want explicit disconnected rejection", w.Code, w.Body)
	}
	if mock.begins != 0 {
		t.Fatalf("activation attempted %d DB writes", mock.begins)
	}
	if s.llmCfg != previous || !s.llmOn || s.llmProf != "previous" || s.chatAgent != previousChat {
		t.Fatal("rejected activation changed previous runtime")
	}
}
