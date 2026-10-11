package codexoauth

import (
	"context"
	"fmt"
	"github.com/Autumn-27/norma/llm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func streamFixture(t *testing.T, body string) (llm.Provider, func()) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	if e := a.save(tokens{Access: jwt(), Refresh: "r", Account: "a", Expiry: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	p, e := newProvider(a, llm.Config{Model: "fixture", MaxRetries: -1}, s.URL, s.Client().Transport)
	if e != nil {
		t.Fatal(e)
	}
	return p, s.Close
}
func TestStreamToolStopReasonAndCompleteArguments(t *testing.T) {
	p, close := streamFixture(t, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call1\",\"name\":\"Read\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"path\\\":\\\"x\\\"}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")
	defer close()
	r := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}}
	stop := ""
	for ev, e := range p.Stream(context.Background(), r) {
		if e != nil {
			t.Fatal(e)
		}
		if ev.Type == llm.SEMessageDelta {
			stop = ev.StopReason
		}
	}
	if stop != "tool_use" {
		t.Fatalf("tool stop=%s", stop)
	}
	m, stop, u, e := p.Complete(context.Background(), r)
	if e != nil || stop != "tool_use" || u.OutputTokens != 2 || len(m.ToolUses()) != 1 || string(m.ToolUses()[0].Input) != `{"path":"x"}` {
		t.Fatalf("tools %+v %s %+v %v", m, stop, u, e)
	}
}
func TestFailedOrTruncatedSSEFailsClosed(t *testing.T) {
	for _, tail := range []string{"", "data: {\"type\":\"response.failed\",\"error\":{\"message\":\"fixture-secret\"}}\n\n"} {
		p, close := streamFixture(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"+tail)
		m, _, _, e := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
		close()
		if e == nil || m.Text() != "" || strings.Contains(e.Error(), "fixture-secret") {
			t.Fatalf("fail-open: %v %v", m, e)
		}
	}
}
