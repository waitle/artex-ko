package codexoauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestIncompleteTerminalFailsClosed(t *testing.T) {
	for _, reason := range []string{"content_filter", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" + fmt.Sprintf("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":%q},\"error\":{\"message\":\"fixture-secret\"}}}\n\n", reason)
			assertTerminalFailure(t, body, true)
		})
	}
}

func TestTerminalFailuresAfterPartialTool(t *testing.T) {
	partial := "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"Read\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\"}\n\n"
	for _, tail := range []string{
		`{"type":"response.failed","response":{"status":"failed","error":{"message":"fixture-secret"}}}`,
		`{"type":"error","message":"fixture-secret"}`,
		`{"type":"response.completed","response":{"status":"incomplete"}}`,
		`{"type":"response.completed","response":{"status":"failed"}}`,
	} {
		t.Run(tail, func(t *testing.T) { assertTerminalFailure(t, partial+"data: "+tail+"\n\n", false) })
	}
}

func TestTruncatedAndInvalidSSEFramesFailClosed(t *testing.T) {
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	for _, tail := range []string{
		"", "data: [DONE]\n\n", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n",
		"data: {\"type\":\"response.completed\"", "data: invalid fixture-secret\n\n",
		"event: response.incomplete\ndata: {}\n\n",
		"data: " + strings.Repeat("x", 1<<20) + "\n\n",
	} {
		t.Run(fmt.Sprintf("tail-length-%d-%q", len(tail), tail[:min(len(tail), 20)]), func(t *testing.T) { assertTerminalFailure(t, partial+tail, true) })
	}
}

func TestCompletedMultilineJSONSequentialToolsAndUsage(t *testing.T) {
	body := ": heartbeat\r\n\r\n"
	for i, arg := range []string{`{"path":"a"}`, `{"path":"b"}`} {
		body += fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"c%d\",\"name\":\"Read\"}}\r\n\r\n", i)
		body += fmt.Sprintf("data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":%q}\r\n\r\n", arg)
	}
	// Status vocabulary inside ordinary content must not trip the guard.
	body += "data: {\"type\":\"response.output_text.delta\",\"delta\":\"response.incomplete error\"}\r\n\r\n"
	body += "event: response.completed\r\ndata: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":2}}}}\r\n\r\n"
	p, close := streamFixture(t, body)
	defer close()
	m, stop, usage, err := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
	tools := m.ToolUses()
	if err != nil || stop != "tool_use" || len(tools) != 2 || tools[0].ID != "c0" || tools[1].ID != "c1" || string(tools[0].Input) != `{"path":"a"}` || string(tools[1].Input) != `{"path":"b"}` || usage.InputTokens != 8 || usage.OutputTokens != 3 || m.Text() != "response.incomplete error" {
		t.Fatalf("completed response corrupted: %+v %q %+v %v", m, stop, usage, err)
	}
}

func TestCancellationInterruptsUnfinishedSSEFrame(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {")
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	a := New(filepath.Join(t.TempDir(), "auth.json"))
	if err := a.save(tokens{Access: jwt(), Refresh: "r", Account: "a", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p, err := newProvider(a, llm.Config{Model: "fixture", MaxRetries: -1}, s.URL, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		m, stop, usage, err := p.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
		if !reflect.DeepEqual(m, llm.Message{}) || stop != "" || !reflect.DeepEqual(usage, llm.Usage{}) {
			done <- fmt.Errorf("aggregate survived cancellation")
			return
		}
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled response succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("frame reader ignored cancellation")
	}
}

func assertTerminalFailure(t *testing.T, body string, wantPartial bool) {
	t.Helper()
	p, close := streamFixture(t, body)
	defer close()
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}}
	failed, partial, stopped := false, false, false
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			failed = true
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("secret leaked: %v", err)
			}
		}
		if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEToolInputJSON {
			t.Fatal("unsafe stream exposed execution-bearing tool event")
		}
		partial = partial || ev.Type == llm.SETextDelta
		stopped = stopped || ev.StopReason != ""
	}
	if !failed || stopped || (wantPartial && !partial) {
		t.Fatalf("failed=%v partial=%v stopped=%v", failed, partial, stopped)
	}
	m, stop, usage, err := p.Complete(context.Background(), req)
	if err == nil || !reflect.DeepEqual(m, llm.Message{}) || stop != "" || !reflect.DeepEqual(usage, llm.Usage{}) {
		t.Fatalf("partial aggregate accepted: %+v %q %+v %v", m, stop, usage, err)
	}
}
