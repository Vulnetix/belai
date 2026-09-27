package kanban

import "sync"

// Source is the live provenance a session's kanban writes are stamped with.
// The TUI's session id changes on /clear and resume while the tools it built
// live on, so the tools read it at call time rather than capturing it.
type Source struct {
	mu sync.RWMutex
	p  Provenance
}

// NewSource returns a source holding p.
func NewSource(p Provenance) *Source { return &Source{p: p} }

// Get returns the current provenance.
func (s *Source) Get() Provenance {
	if s == nil {
		return Provenance{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

// SetSession records the session id now writing.
func (s *Source) SetSession(id string) {
	s.mu.Lock()
	s.p.SessionID = id
	s.mu.Unlock()
}

// Set replaces the provenance (a workdir change).
func (s *Source) Set(p Provenance) {
	s.mu.Lock()
	s.p = p
	s.mu.Unlock()
}
