package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/repoindex"
	"github.com/vulnetix/signet/internal/sanitize"
)

// Grounding is the always-useful, read-only workspace evidence attached to
// every explore subagent. It is untrusted: it re-enters the conversation as
// part of the subagent prompt and is admitted through the Role Manager before
// the subagent's own model turn, never promoted into a system/agent block.
type Grounding struct {
	GitStatus     string
	Branch        string
	RecentCommits string
	Layout        string
	AgentsMD      string
	AgentNames    []string
	SiblingRepos  []repoindex.Entry
}

const (
	groundingTimeout  = 3 * time.Second
	groundingMaxBytes = 16 * 1024
	groundingMaxList  = 200
)

// groundingProbe runs the short, non-mutating grounding probe: git state, a
// bounded top-level directory listing, AGENTS.md, and the relevant background
// agents. Every step is read-only, capped, and timed out; a failure is silent
// (the field stays empty) rather than aborting exploration.
func (s *Session) groundingProbe(ctx context.Context) Grounding {
	g := Grounding{
		GitStatus:     repoindex.RunProbe(ctx, s.workdir, "git", "status", "--short"),
		Branch:        repoindex.RunProbe(ctx, s.workdir, "git", "rev-parse", "--abbrev-ref", "HEAD"),
		RecentCommits: repoindex.RunProbe(ctx, s.workdir, "git", "log", "--oneline", "-5"),
		Layout:        listLayout(s.workdir),
		AgentsMD:      readAgentsMD(s.workdir),
		AgentNames:    relevantAgents(),
		SiblingRepos:  s.repoIndex.Entries(),
	}
	return g
}

// digest renders the grounding evidence as one sanitized text block for the
// subagent prompt. It is model input, so it is sanitized exactly like any
// other user-supplied content.
func (g Grounding) digest() string {
	var b strings.Builder
	if g.Branch != "" || g.GitStatus != "" {
		b.WriteString("Repository state:\n")
		if g.Branch != "" {
			b.WriteString("branch: " + g.Branch + "\n")
		}
		if g.GitStatus != "" {
			b.WriteString("git status --short:\n" + g.GitStatus + "\n")
		}
		if g.RecentCommits != "" {
			b.WriteString("recent commits:\n" + g.RecentCommits + "\n")
		}
	}
	if g.Layout != "" {
		b.WriteString("Top-level layout:\n" + g.Layout + "\n")
	}
	if g.AgentsMD != "" {
		b.WriteString("AGENTS.md:\n" + g.AgentsMD + "\n")
	}
	if len(g.AgentNames) > 0 {
		b.WriteString("Available background agents:\n" + strings.Join(g.AgentNames, ", ") + "\n")
	}
	if len(g.SiblingRepos) > 0 {
		b.WriteString("\nLocally available repositories (read these from disk with Repos/RepoFiles/RepoRead rather than calling GH):\n")
		for _, e := range g.SiblingRepos {
			b.WriteString("  " + e.String() + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return sanitize.Sanitize(b.String())
}

// listLayout returns a bounded top-level directory listing, one entry per
// line with directories marked.
func listLayout(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > groundingMaxList {
		names = names[:groundingMaxList]
	}
	return strings.Join(names, "\n")
}

// readAgentsMD returns AGENTS.md content, capped. Missing file is silent.
func readAgentsMD(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return ""
	}
	if len(b) > groundingMaxBytes {
		b = b[:groundingMaxBytes]
	}
	return strings.TrimRight(string(b), "\n")
}

// relevantAgents lists the user's background agents that are not scheduled,
// loop, or monitor definitions — i.e. the flat, single-shot profiles an
// explore subagent could plausibly consult. Scheduled/loop/monitor definitions
// are background processes, not evidence to attach.
func relevantAgents() []string {
	profiles, err := agentprofile.List()
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range profiles {
		if !p.Builtin && p.Mode == agentprofile.ModeSingle {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}
