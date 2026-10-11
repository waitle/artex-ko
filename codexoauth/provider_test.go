package codexoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/norma/llm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderForcesCodexShapeAndCollectsSDKStream(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	if e := a.save(tokens{Access: jwt(), Refresh: "r", Account: "account", Expiry: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		input, _ := json.Marshal(b["input"])
		for _, want := range []string{"earlier question", "function_call", "history-call", "function_call_output", "history result", "ping"} {
			if !strings.Contains(string(input), want) {
				t.Errorf("history missing %q: %s", want, input)
			}
		}
		if b["parallel_tool_calls"] != false {
			t.Errorf("parallel tool calls must be disabled with indexless SDK events: %v", b)
		}
		if b["stream"] != true || b["store"] != false || b["instructions"] != "" || b["max_output_tokens"] != nil || b["temperature"] != nil {
			t.Errorf("body: %v", b)
		}
		if r.Header.Get("ChatGPT-Account-ID") != "account" {
			t.Error("missing account")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
	}))
	defer s.Close()
	p, e := newProvider(a, llm.Config{Model: "fixture", MaxRetries: -1}, s.URL, s.Client().Transport)
	if e != nil {
		t.Fatal(e)
	}
	m, _, u, e := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{
		llm.UserText("earlier question"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "history-call", Name: "Read", Input: json.RawMessage(`{"path":"history"}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("history-call", "history result", false)}},
		llm.UserText("ping"),
	}, MaxTokens: 10})
	if e != nil || m.Text() != "OK" || u.OutputTokens != 1 {
		t.Fatalf("result %+v %+v %v", m, u, e)
	}
}
