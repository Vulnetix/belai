package tui

import (
	"sync"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/run"
)

// noncePools keeps one seeded nonce pool per nonce endpoint for the life of
// the App. The TUI rebuilds its agent session on many triggers (a mode,
// guardrail or MCP change, a resume); reusing the pool keeps the sealed
// system prompt's bytes unchanged across a rebuild that changes nothing it
// renders, so the provider still serves the prefix from cache, and saves a
// nonce GET per rebuild. A provider or route change selects another pool.
type noncePools struct {
	mu sync.Mutex
	m  map[string]*nonce.Pool
	// memos keep each endpoint's sealed system prompt beside its pool:
	// sealing draws fresh nonces, so the pool alone did not keep the bytes.
	memos map[string]*agent.SealMemo
}

func noncePoolKey(cfg run.Config) string {
	return cfg.Provider + "\x00" + cfg.BaseURL
}

// get returns the pool kept for cfg's endpoint, or nil. A nil receiver keeps
// nothing.
func (n *noncePools) get(cfg run.Config) *nonce.Pool {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.m[noncePoolKey(cfg)]
}

// keep records the pool a session built for cfg's endpoint.
func (n *noncePools) keep(cfg run.Config, p *nonce.Pool) {
	if n == nil || p == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.m == nil {
		n.m = map[string]*nonce.Pool{}
	}
	n.m[noncePoolKey(cfg)] = p
}

// memo returns the sealed-prompt memo kept for cfg's endpoint, creating it on
// first use. A nil receiver keeps nothing.
func (n *noncePools) memo(cfg run.Config) *agent.SealMemo {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.memos == nil {
		n.memos = map[string]*agent.SealMemo{}
	}
	k := noncePoolKey(cfg)
	if n.memos[k] == nil {
		n.memos[k] = &agent.SealMemo{}
	}
	return n.memos[k]
}
