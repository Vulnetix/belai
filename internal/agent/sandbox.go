package agent

import "github.com/vulnetix/belai/internal/sandbox"

// sandboxPolicy is the OS sandbox policy for the next command: the settings,
// the session's current workspace roots, and the live guardrails switch
// (off takes the sandbox with it). Only tools that spawn commands read it.
// A fleet worker's session adds the narrow git paths its worktree needs
// (Options.SandboxMounts).
func (s *Session) sandboxPolicy() sandbox.Policy {
	roots := s.registry.Cwd().Roots()
	if len(roots) == 0 && s.workdir != "" {
		roots = []string{s.workdir}
	}
	p := sandbox.FromSettings(s.settings.Sandbox, roots, s.live.Policy())
	if p.Mode != sandbox.ModeOff {
		p.Mounts = append(p.Mounts, s.sandboxMounts...)
		p.Env = append(p.Env, s.sandboxEnv...)
	}
	return p
}
