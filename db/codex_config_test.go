package db

import (
	"strings"
	"testing"
)

func TestCodexSchemaFreshAndMigration(t *testing.T) {
	if strings.Count(schemaSQL, "'openai','anthropic','openai-responses','openai-codex'") != 2 {
		t.Fatal("fresh and migration checks do not accept Codex")
	}
}
func TestCodexProfileRejectsCallerCredentialsBeforeDatabase(t *testing.T) {
	d := &DB{}
	if _, e := d.SaveProfile(&LLMProfile{Format: "openai-codex", Model: "fixture", APIKey: "fake"}); e == nil {
		t.Fatal("caller credentials accepted")
	}
}
