package codexoauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/norma/llm"
	"io"
	"iter"
	"net/http"
	"strings"
)

type transport struct {
	auth   *Auth
	base   http.RoundTripper
	origin string
}

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != t.origin+"/responses" {
		return nil, errors.New("Codex credential destination rejected")
	}
	ctx, token, account, cleanup, e := t.auth.beginRequest(r.Context())
	if e != nil {
		return nil, e
	}
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()
	raw, e := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if e != nil {
		return nil, errors.New("Codex request invalid")
	}
	r.Body.Close()
	var b map[string]any
	if json.Unmarshal(raw, &b) != nil {
		return nil, errors.New("Codex request invalid")
	}
	b["stream"] = true
	b["store"] = false
	b["parallel_tool_calls"] = false // Norma events omit output indexes; preserve deterministic tool argument ownership.
	if b["instructions"] == nil {
		b["instructions"] = ""
	}
	b["include"] = []string{"reasoning.encrypted_content"}
	for _, k := range []string{"temperature", "top_p", "max_output_tokens", "max_tokens", "max_completion_tokens", "stop"} {
		delete(b, k)
	}
	raw, _ = json.Marshal(b)
	r = r.Clone(ctx)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.GetBody = nil
	r.Header = make(http.Header)
	r.Header.Set("Content-Type", "application/json")
	headers(r, token, account)
	r.Header.Set("Accept", "text/event-stream")
	resp, e := t.base.RoundTrip(r)
	if e != nil {
		return nil, errors.New("Codex backend unavailable")
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cleanup: cleanup}
	handedOff = true
	if resp.StatusCode != 200 {
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"error":{"message":"Codex request rejected (HTTP %d)"}}`, resp.StatusCode)))
		resp.ContentLength = -1
	} else {
		resp.Body = &terminalBody{ReadCloser: resp.Body, reader: bufio.NewReader(resp.Body)}
	}
	return resp, nil
}
func headers(r *http.Request, token, account string) {
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("ChatGPT-Account-ID", account)
	r.Header.Set("originator", "codex_cli_rs")
	r.Header.Set("User-Agent", "codex_cli_rs/0.0.0 (ARTEX)")
	r.Header.Set("OpenAI-Beta", "responses=experimental")
}
func NewProvider(a *Auth, c llm.Config) (llm.Provider, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return newProvider(a, c, Backend, tr)
}
func newProvider(a *Auth, c llm.Config, origin string, tr http.RoundTripper) (llm.Provider, error) {
	if a == nil {
		return nil, ErrDisconnected
	}
	c.Format = llm.FormatOpenAIResponses
	c.BaseURL = origin
	c.APIKey = "oauth-managed"
	c.HTTPClient = &http.Client{Transport: transport{a, tr, origin}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p, e := llm.NewProvider(c)
	if e != nil {
		return nil, e
	}
	return provider{p}, nil
}

// terminalBody validates one bounded SSE frame before exposing it to Norma,
// which normalizes response.incomplete to a successful stop reason. No response
// history is retained; frame/data/JSON allocations are bounded by maxSSEFrame.
const maxSSEFrame = 1 << 20

type terminalBody struct {
	io.ReadCloser
	reader    *bufio.Reader
	pending   []byte
	completed bool
	err       error
}

func (b *terminalBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(b.pending) == 0 && b.err == nil {
		b.pending, b.err = b.frame()
	}
	if len(b.pending) != 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	return 0, b.err
}

func (b *terminalBody) frame() ([]byte, error) {
	var frame []byte
	lineStart := 0
	for {
		part, err := b.reader.ReadSlice('\n')
		if len(frame)+len(part) > maxSSEFrame {
			return nil, errors.New("Codex response frame too large")
		}
		frame = append(frame, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(frame) == 0 && b.completed {
				return nil, io.EOF
			}
			return nil, errors.New("Codex response stream incomplete")
		}
		line := bytes.TrimRight(frame[lineStart:], "\r\n")
		lineStart = len(frame)
		if len(line) == 0 {
			break
		}
	}
	var data []byte
	var event string
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("event:")) {
			event = strings.TrimSpace(string(line[6:]))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, bytes.TrimPrefix(line[5:], []byte(" "))...)
			data = append(data, '\n')
		}
	}
	// Never surface upstream error payloads, which can contain credentials or
	// private prompt/tool content. Match parsed fields, not substrings in text.
	failed := func(s string) bool {
		return s == "response.failed" || s == "response.incomplete" || s == "error" || s == "failed" || s == "incomplete"
	}
	if failed(event) {
		return nil, errors.New("Codex response stream failed")
	}
	if len(data) == 0 {
		return frame, nil
	}
	if strings.TrimSpace(string(data)) == "[DONE]" {
		if !b.completed {
			return nil, errors.New("Codex response stream incomplete")
		}
		return frame, nil
	}
	var status struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &status) != nil {
		return nil, errors.New("Codex response frame invalid")
	}
	if failed(status.Type) || failed(status.Response.Status) {
		return nil, errors.New("Codex response stream failed")
	}
	if status.Type == "response.completed" {
		if status.Response.Status != "completed" {
			return nil, errors.New("Codex response stream incomplete")
		}
		b.completed = true
	}
	return frame, nil
}

type provider struct{ inner llm.Provider }
type cancelBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *cancelBody) Close() error { e := b.ReadCloser.Close(); b.cleanup(); return e }

// Hold the execution-bearing suffix until the SDK exhausts the validated wire
// stream. Norma can execute tools on both a following event and a stream error.
const maxHeldBytes = 4 << 20
const maxHeldEvents = 4096

func (p provider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		done, hasTool := false, false
		var held []llm.StreamEvent
		heldBytes := 0
		var input strings.Builder
		building := false
		validTool := func() bool {
			if !building {
				return true
			}
			building = false
			valid := json.Valid([]byte(input.String()))
			input.Reset()
			return valid
		}
		fail := func() { yield(llm.StreamEvent{}, errors.New("Codex response stream failed")) }
		for ev, e := range p.inner.Stream(ctx, r) {
			if e != nil || ctx.Err() != nil {
				fail()
				return
			}
			if ev.Type != llm.SEToolInputJSON && !validTool() {
				fail()
				return
			}
			if ev.Type == llm.SEToolUseStart {
				hasTool, building = true, true
			}
			if ev.Type == llm.SEMessageDelta && hasTool && ev.StopReason == "end_turn" {
				ev.StopReason = "tool_use"
			}
			if ev.Type == llm.SEMessageDelta && ev.StopReason != "" {
				done = true
			}
			if hasTool || ev.Type == llm.SEToolInputJSON {
				// Conservative fixed storage allowance includes event slice growth;
				// charge input twice for its retained JSON-validation copy too.
				size := 512 + len(ev.Type) + len(ev.Text) + len(ev.ToolID) + len(ev.ToolName) + len(ev.StopReason)
				if ev.Type == llm.SEToolInputJSON {
					size += len(ev.Text)
				}
				if len(held) >= maxHeldEvents || size > maxHeldBytes-heldBytes {
					fail()
					return
				}
				heldBytes += size
				held = append(held, ev)
				if ev.Type == llm.SEToolInputJSON {
					if !building {
						fail()
						return
					}
					input.WriteString(ev.Text)
				}
			} else if !yield(ev, nil) {
				return
			}
		}
		if ctx.Err() != nil || !validTool() {
			fail()
			return
		}
		if !done {
			yield(llm.StreamEvent{}, errors.New("Codex response stream incomplete"))
			return
		}
		for _, ev := range held {
			if ctx.Err() != nil {
				fail()
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}
func (p provider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	m := llm.Message{Role: llm.RoleAssistant}
	var u llm.Usage
	stop := "end_turn"
	idx := -1
	hasTool := false
	for ev, e := range p.Stream(ctx, r) {
		if e != nil {
			return llm.Message{}, "", llm.Usage{}, e
		}
		switch ev.Type {
		case llm.SETextDelta:
			if idx < 0 || m.Content[idx].Type != llm.BlockText {
				m.Content = append(m.Content, llm.TextBlock(""))
				idx = len(m.Content) - 1
			}
			m.Content[idx].Text += ev.Text
		case llm.SEThinkingDelta:
			if idx < 0 || m.Content[idx].Type != llm.BlockThinking {
				m.Content = append(m.Content, llm.ContentBlock{Type: llm.BlockThinking})
				idx = len(m.Content) - 1
			}
			m.Content[idx].Thinking += ev.Text
		case llm.SEToolUseStart:
			hasTool = true
			m.Content = append(m.Content, llm.ContentBlock{Type: llm.BlockToolUse, ID: ev.ToolID, Name: ev.ToolName})
			idx = len(m.Content) - 1
		case llm.SEToolInputJSON:
			if idx < 0 || m.Content[idx].Type != llm.BlockToolUse {
				return llm.Message{}, "", u, errors.New("Codex tool stream invalid")
			}
			m.Content[idx].Input = append(m.Content[idx].Input, ev.Text...)
		case llm.SEMessageStart, llm.SEMessageDelta:
			u.Add(ev.Usage)
			if ev.StopReason != "" {
				stop = ev.StopReason
			}
		}
	}
	if ctx.Err() != nil {
		return llm.Message{}, "", llm.Usage{}, ctx.Err()
	}
	for i := range m.Content {
		b := &m.Content[i]
		if b.Type == llm.BlockToolUse {
			if len(b.Input) == 0 {
				b.Input = json.RawMessage(`{}`)
			}
			if !json.Valid(b.Input) {
				return llm.Message{}, "", llm.Usage{}, errors.New("Codex tool arguments invalid")
			}
		}
	}
	if hasTool && stop == "end_turn" {
		stop = "tool_use"
	}
	return m, stop, u, nil
}
func (a *Auth) Models(ctx context.Context) ([]string, error) {
	ctx, token, account, cleanup, e := a.beginRequest(ctx)
	if e != nil {
		return nil, e
	}
	defer cleanup()
	r, _ := http.NewRequestWithContext(ctx, "GET", Backend+"/models?client_version=0.0.0", nil)
	headers(r, token, account)
	r.Header.Set("Accept", "application/json")
	resp, e := a.client.Do(r)
	if e != nil {
		return nil, errors.New("Codex model catalog unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Codex model catalog rejected (HTTP %d)", resp.StatusCode)
	}
	var b any
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&b) != nil {
		return nil, errors.New("Codex model catalog invalid")
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	items, ok := b.([]any)
	if obj, isObject := b.(map[string]any); isObject {
		items, ok = obj["models"].([]any)
		if !ok {
			items, ok = obj["data"].([]any)
		}
	}
	if !ok {
		return nil, errors.New("Codex model catalog invalid")
	}
	for _, item := range items {
		switch m := item.(type) {
		case string:
			add(m)
		case map[string]any:
			for _, k := range []string{"slug", "id", "name"} {
				if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
					add(s)
					break
				}
			}
		}
	}
	return out, nil
}
