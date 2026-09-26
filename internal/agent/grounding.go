package agent

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Grounding is the read-only workspace evidence attached to explore subagent
// prompts. It is untrusted: it re-enters the conversation as part of the
// subagent prompt and is admitted through the Role Manager before the
// subagent's own model turn, never promoted into a system/agent block.
//
// It carries only what the subagent's context does not already hold. The
// repository map (layout, languages, commands, entrypoints, the presence of
// AGENTS.md) is in every subagent's system block and the branch and changed
// paths ride on its turn status, so re-sending them — and the whole of
// AGENTS.md, which every subagent's admission call then classified again —
// only made each subagent slower.
type Grounding struct {
	RecentCommits string
	SiblingRepos  []repoindex.Entry
}

// groundingProbe runs the short, non-mutating grounding probe once per
// explore fan-out. A failure is silent (the field stays empty) rather than
// aborting exploration.
func (s *Session) groundingProbe(ctx context.Context) Grounding {
	return Grounding{
		RecentCommits: repoindex.RunProbe(ctx, s.workdir, "git", "log", "--oneline", "-5"),
		SiblingRepos:  s.repoIndex.Entries(),
	}
}

// digest renders the grounding evidence as one sanitized text block for a
// subagent prompt. The local-repository index only helps a task about
// another repository, so it is included only when withRepos is set. It is
// model input, so it is sanitized exactly like any other user-supplied
// content.
func (g Grounding) digest(withRepos bool) string {
	var b strings.Builder
	if g.RecentCommits != "" {
		b.WriteString("recent commits:\n" + g.RecentCommits + "\n")
	}
	if withRepos && len(g.SiblingRepos) > 0 {
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

// groundingMaxList caps listLayout's entries.
const groundingMaxList = 200

// listLayout returns a bounded top-level directory listing, one entry per
// line with directories marked. The clarifier uses it as harness facts.
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
