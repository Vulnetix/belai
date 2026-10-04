package run

import (
	"context"
	"net/http"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

// Workers AI serves prompt-prefix caching per replica, and a request is
// routed to the replica that already holds its prefix only when it carries
// an x-session-affinity header naming the conversation. Without it, every
// call of an agent loop — each resending the same system block, tool
// definitions and growing transcript — lands on a cold replica and prefills
// the whole prompt again: on an 18k-token prompt that was most of each
// call's latency. The session id is the stable key, the same value already
// sent as X-Belai-Session-Id.
//
// The header carries only the opaque session id, never content.

// headerSessionAffinity is the Workers AI prefix-cache routing header.
const headerSessionAffinity = "x-session-affinity"

// wantsSessionAffinity reports whether requests for cfg reach Workers AI,
// directly or through the AI Gateway (whose Claude branch goes to Anthropic
// and is excluded).
func wantsSessionAffinity(cfg Config) bool {
	if d, ok := provider.Lookup(cfg.Provider); ok && d.Surface == wire.SurfaceWorkersAI {
		return true
	}
	if cfg.Provider == "cloudflare-ai-gateway" {
		return !isClaudeModel(cfg.Model)
	}
	return false
}

// applySessionAffinity sets the affinity header when the request goes to
// Workers AI and the context carries a session.
func applySessionAffinity(ctx context.Context, cfg Config, h http.Header) {
	if !wantsSessionAffinity(cfg) {
		return
	}
	if id := calltrace.SessionID(ctx); id != "" {
		h.Set(headerSessionAffinity, id)
	}
}
