package codexoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/norma/llm"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

const completedFixture = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"

func toolFixture(args, tail string) string {
	return "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"BoundaryProbe\"}}\n\n" + fmt.Sprintf("data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":%q}\n\n", args) + tail
}

func assertBoundaryExecutions(t *testing.T, body string, want int32, wantError bool) {
	t.Helper()
	p, close := streamFixture(t, body)
	defer close()
	var executions atomic.Int32
	probe := tool.Build(tool.Spec{Name: "BoundaryProbe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		executions.Add(1)
		return tool.Text("executed"), nil
	}})
	_, err := agentcore.Run(context.Background(), agentcore.Options{Provider: p, Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir(), MaxTurns: 1, DisableBackgroundTasks: true}, "probe")
	t.Logf("actual Norma agentcore executions=%d error=%v", executions.Load(), err)
	if executions.Load() != want {
		t.Fatalf("tool execution boundary violated: executions=%d want=%d", executions.Load(), want)
	}
	if wantError && err == nil {
		t.Fatal("unsafe response succeeded")
	}
}

func TestToolExecutionBoundaryCompleted(t *testing.T) {
	assertBoundaryExecutions(t, toolFixture(`{}`, completedFixture), 1, false)
}

func TestToolExecutionBoundaryInvalidCompleted(t *testing.T) {
	assertBoundaryExecutions(t, toolFixture(`{`, completedFixture), 0, true)
}

func TestToolExecutionBoundaryFailedAndTruncated(t *testing.T) {
	for _, tail := range []string{"", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", "data: {\"type\":\"error\",\"message\":\"fixture-secret\"}\n\n", completedFixture + "data: invalid\n\n"} {
		t.Run(fmt.Sprintf("tail-%d", len(tail)), func(t *testing.T) { assertBoundaryExecutions(t, toolFixture(`{}`, tail), 0, true) })
	}
}

// Keep the HTTP stream open after a tool and a following text event (which
// causes Norma's streaming scheduler to finalize and dispatch a tool). The
// preterminal counter proves absence of execution while the model is pending.
func TestToolExecutionBoundaryPendingTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		want       int32
		cancel     bool
	}{
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", 0, false},
		{"completed", completedFixture, 1, false},
		{"cancelled", completedFixture, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executions, beforeTerminal atomic.Int32
			called := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, toolFixture(`{}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"after-tool\"}\n\n"))
				w.(http.Flusher).Flush()
				select {
				case <-called:
				case <-time.After(100 * time.Millisecond):
				}
				beforeTerminal.Store(executions.Load())
				if tc.cancel {
					cancel()
				}
				fmt.Fprint(w, tc.tail)
			}))
			defer server.Close()
			a := New(filepath.Join(t.TempDir(), "auth.json"))
			if err := a.save(tokens{Access: jwt(), Refresh: "r", Account: "a", Expiry: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			p, err := newProvider(a, llm.Config{Model: "fixture", MaxRetries: -1}, server.URL, server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			probe := tool.Build(tool.Spec{Name: "BoundaryProbe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
				executions.Add(1)
				called <- struct{}{}
				return tool.Text("executed"), nil
			}})
			_, err = agentcore.Run(ctx, agentcore.Options{Provider: p, Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir(), MaxTurns: 1, DisableBackgroundTasks: true}, "probe")
			t.Logf("actual Norma executions before terminal=%d final=%d error=%v", beforeTerminal.Load(), executions.Load(), err)
			if beforeTerminal.Load() != 0 || executions.Load() != tc.want {
				t.Fatalf("execution boundary violated: before=%d final=%d want=%d", beforeTerminal.Load(), executions.Load(), tc.want)
			}
			if tc.want == 0 && err == nil {
				t.Fatal("unsafe response succeeded")
			}
		})
	}
}

func TestToolBufferBounds(t *testing.T) {
	for _, tc := range []struct{ name, suffix string }{
		{"bytes", strings.Repeat("data: {\"type\":\"response.output_text.delta\",\"delta\":\""+strings.Repeat("x", 1<<18)+"\"}\n\n", 17)},
		{"events", strings.Repeat("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n", maxHeldEvents)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertBoundaryExecutions(t, toolFixture(`{}`, tc.suffix+completedFixture), 0, true)
		})
	}
}

func TestToolArgumentBufferBounds(t *testing.T) {
	// Input fragments are retained both as events and in the JSON validator.
	body := toolFixture(`{"value":"`, "")
	for range 9 {
		body += fmt.Sprintf("data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":%q}\n\n", strings.Repeat("x", 1<<18))
	}
	body += "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"\\\"}\"}\n\n" + completedFixture
	assertBoundaryExecutions(t, body, 0, true)
}

func TestToolBoundaryRejectsAllWhenLaterToolInvalid(t *testing.T) {
	body := toolFixture(`{}`, "") + strings.ReplaceAll(toolFixture(`{`, completedFixture), `"c1"`, `"c2"`)
	assertBoundaryExecutions(t, body, 0, true)
}

func TestToolBoundaryRejectsMissingArguments(t *testing.T) {
	assertBoundaryExecutions(t, toolFixture("", completedFixture), 0, true)
}

func TestToolBoundarySequentialExecutions(t *testing.T) {
	body := toolFixture(`{}`, "") + strings.ReplaceAll(toolFixture(`{}`, completedFixture), `"c1"`, `"c2"`)
	assertBoundaryExecutions(t, body, 2, false)
}

func TestToolBufferPreservesNormalizedOrder(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"before\"}\n\n" + toolFixture(`{}`, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"middle\"}\n\n") + strings.ReplaceAll(toolFixture(`{}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"after\"}\n\n"+completedFixture), `"c1"`, `"c2"`)
	p, close := streamFixture(t, body)
	defer close()
	var got []string
	for ev, err := range p.Stream(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s|%s|%s|%s", ev.Type, ev.Text, ev.ToolID, ev.StopReason))
	}
	want := []string{"text_delta|before||", "tool_use_start||c1|", "tool_input_delta|{}||", "thinking_delta|middle||", "tool_use_start||c2|", "tool_input_delta|{}||", "text_delta|after||", "message_delta|||tool_use"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("normalized order changed: got=%q want=%q", got, want)
	}
}

func TestToolExecutionBoundaryIncomplete(t *testing.T) {
	assertBoundaryExecutions(t, toolFixture("{", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n"), 0, true)
}
