package run

import (
	"context"
	"net/http"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
)

func TestSessionAffinityOnlyForWorkersAI(t *testing.T) {
	ctx := calltrace.WithSession(context.Background(), "sess-123")
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Provider: "cloudflare-ai-gateway", Model: "workers-ai/@cf/moonshotai/kimi-k2.7-code"}, "sess-123"},
		{Config{Provider: "cloudflare-workers-ai", Model: "@cf/meta/llama"}, "sess-123"},
		{Config{Provider: "cloudflare-ai-gateway", Model: "claude-sonnet-4-5"}, ""},
		{Config{Provider: "openai", Model: "gpt-5"}, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		applySessionAffinity(ctx, c.cfg, h)
		if got := h.Get("x-session-affinity"); got != c.want {
			t.Errorf("%s/%s: affinity %q, want %q", c.cfg.Provider, c.cfg.Model, got, c.want)
		}
	}
	h := http.Header{}
	applySessionAffinity(context.Background(), cases[0].cfg, h)
	if h.Get("x-session-affinity") != "" {
		t.Error("affinity sent without a session")
	}
}
