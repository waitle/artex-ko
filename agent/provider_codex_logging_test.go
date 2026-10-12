package agent

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
)

func TestCodexConnectionCaptureDoesNotReportUnsentHTTP(t *testing.T) {
	old := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	defer log.SetOutput(old)
	_, capture := llmrec.NewCapture(context.Background())
	logTestConnection(ConfigFrom("openai-codex", "fixture-model", "", "", ""), capture)
	if strings.Contains(output.String(), "未发出任何 HTTP 请求") {
		t.Fatalf("OAuth transport deliberately bypasses raw capture; emitted false failure: %s", output.String())
	}
}

func TestKeyProviderEmptyCaptureStillReportsUnsentHTTP(t *testing.T) {
	old := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	defer log.SetOutput(old)
	_, capture := llmrec.NewCapture(context.Background())
	logTestConnection(ConfigFrom("openai-responses", "fixture-model", "", "fixture-key", ""), capture)
	if !strings.Contains(output.String(), "未发出任何 HTTP 请求") {
		t.Fatalf("lost existing key-provider diagnostics: %s", output.String())
	}
}
