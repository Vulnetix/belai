package proc

import (
	"strings"
	"testing"
)

func TestScrubbedEnvStripsSensitiveAndKeepsPlain(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "cf-id")
	t.Setenv("MY_API_KEY", "key")
	t.Setenv("MY_TOKEN", "tok")
	t.Setenv("MY_SECRET", "sec")
	t.Setenv("SIGNET_SETTINGS", "/tmp")
	t.Setenv("MY_PLAIN_VAR", "keep-me")

	got := strings.Join(ScrubbedEnv(), "\n")
	for _, key := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CLOUDFLARE_ACCOUNT_ID",
		"MY_API_KEY", "MY_TOKEN", "MY_SECRET", "SIGNET_SETTINGS",
	} {
		if strings.Contains(got, key+"=") {
			t.Fatalf("sensitive key %s leaked in ScrubbedEnv", key)
		}
	}
	if !strings.Contains(got, "MY_PLAIN_VAR=keep-me") {
		t.Fatalf("plain key missing from ScrubbedEnv")
	}
}
