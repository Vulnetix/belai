package agent

import "github.com/vulnetix/signet/internal/sandbox"

// sandboxPolicy is the OS sandbox policy for the next command: the settings,
// the session's current workspace roots, and the live guardrails switch
// (off takes the sandbox with it). Only tools that spawn commands read it.
func (s *Session) sandboxPolicy() sandbox.Policy {
	roots := s.registry.Cwd().Roots()
	if len(roots) == 0 && s.workdir != "" {
		roots = []string{s.workdir}
	}
	return sandbox.FromSettings(s.settings.Sandbox, roots, s.live.Policy())
}
