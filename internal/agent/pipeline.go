package agent

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/vulnetix/belai/internal/resilience"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// sessionPipeline holds the classifier pipeline a session reuses across turns
// and tool results. Building one is not free: it resolves the role and guard
// classifiers, and a routed role classifier keeps its per-use-case route in
// memory, so a pipeline rebuilt every call asked the router again every turn.
// It is rebuilt only when the config it was built from changes.
type sessionPipeline struct {
	mu   sync.Mutex
	key  [32]byte
	pipe *rolemanager.Pipeline
	// builds counts constructions, for tests.
	builds atomic.Int32
	// emit is where the pipeline's retry notices go: the latest caller's.
	emit atomic.Pointer[func(Event)]
}

// pipeline returns the session's classifier pipeline, built from the current
// config on first use and again only after the config changes. Retries are
// reported to emit.
func (s *Session) pipeline(emit func(Event)) *rolemanager.Pipeline {
	sp := &s.pipe
	sp.emit.Store(&emit)
	key := sha256.Sum256([]byte(fmt.Sprintf("%#v", s.cfg)))
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.pipe != nil && sp.key == key {
		return sp.pipe
	}
	p := run.NewPipelineWithRetry(s.cfg, s.client, s.cache, func(a resilience.Attempt) {
		if e := sp.emit.Load(); e != nil {
			(*e)(Event{Kind: EventRetryKind, RetryAttempt: a.Attempt, RetryMax: a.Max, RetryDelay: a.Delay, RetryReason: a.Reason})
		}
	})
	// The Role Manager owns the FIFO fan-out pool and reaches it through the
	// pipeline so queue admission is traced with the rolemanager record helper.
	p.Pool = s.agentPool
	sp.pipe, sp.key = p, key
	sp.builds.Add(1)
	return p
}
