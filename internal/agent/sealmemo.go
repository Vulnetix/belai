package agent

import (
	"fmt"
	"sync"

	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/run"
)

// SealMemo memoises the sealed system prompt. Sealing draws fresh nonces, so a
// prompt sealed again with nothing changed still differs byte for byte and
// misses the provider's prompt cache from that block onward. A session keeps
// one across its turns; the TUI keeps one beside the nonce pool it keeps
// across session rebuilds (Options.SealMemo), so the turn after a mode switch
// or a plan-mode turn sends the same prefix as the turn before it.
type SealMemo struct {
	mu     sync.Mutex
	key    string
	sealed string
}

// sealSystem returns the sealed system and tools blocks for opts, reusing the
// previous turn's sealed bytes when nothing that feeds them changed. Sealing
// is keyed on the provider, the model, the options and the pool the nonces
// came from: a sealed block is only valid against the pool that minted it.
func (s *Session) sealSystem(opts prompt.Options) (string, error) {
	if s.seal == nil {
		// A session built without NewSession (a test, a subagent literal).
		s.seal = &SealMemo{}
	}
	m := s.seal
	key := fmt.Sprintf("%p\x00%s\x00%s\x00%#v", s.pool, s.cfg.Provider, s.cfg.Model, opts)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealed != "" && m.key == key {
		return m.sealed, nil
	}
	system, err := run.SealSystem(s.cfg, s.pool, opts)
	if err != nil {
		return "", err
	}
	m.key, m.sealed = key, system
	return system, nil
}

// SealMemo returns the memo the session seals with, so a rebuilt session can
// keep it (Options.SealMemo).
func (s *Session) SealMemo() *SealMemo { return s.seal }
