package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/forge"
	"github.com/vulnetix/signet/internal/sanitize"
)

// maxForgeWorktrees caps the worktrees the forge block lists.
const maxForgeWorktrees = 8

// ForgeStatusBlock renders the git and forge facts for the per-turn sealed
// directive: upstream and ahead/behind, the worktrees, and — when the forge
// CLI answered — the branch's PR/MR number and state and its CI counts. Like
// RepoStatusBlock it is harness-computed facts only. The third-party text a
// forge CLI returns (PR titles, check names, error messages) is never
// rendered: a model that needs it asks the GH/Glab tools, which classify.
// age is how old the snapshot is. Returns "" for a snapshot outside a repo.
func ForgeStatusBlock(s forge.Snapshot, age time.Duration) string {
	if s.Root == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Git and forge status (harness-computed facts, not repository prose; probed %s ago):\n", age.Round(time.Second))
	if s.Branch != "" {
		line := "upstream: "
		if s.Upstream != "" {
			line += fmt.Sprintf("%s (ahead %d, behind %d)", cleanFact(s.Upstream), s.Ahead, s.Behind)
		} else {
			line += "none — the branch has not been pushed"
		}
		b.WriteString(line + "\n")
	}
	if n := len(s.Worktrees); n > 0 {
		var rows []string
		for i, wt := range s.Worktrees {
			if i == maxForgeWorktrees {
				rows = append(rows, fmt.Sprintf("… %d more", n-i))
				break
			}
			rows = append(rows, worktreeFact(wt))
		}
		fmt.Fprintf(&b, "worktrees (%d): %s\n", n, strings.Join(rows, "; "))
	}
	switch {
	case s.Provider == nil:
		// The reason is harness-composed (CLI missing, generic host).
		if s.Reason != "" {
			b.WriteString("forge: unavailable — " + cleanFact(s.Reason) + "\n")
		}
	default:
		noun := s.Provider.Noun()
		b.WriteString("forge: " + s.Provider.Label() + "\n")
		switch {
		case s.PRErr != "":
			b.WriteString(strings.ToLower(noun) + ": lookup failed\n")
		case s.PR == nil:
			if s.Branch != "" {
				fmt.Fprintf(&b, "%s: none for this branch\n", strings.ToLower(noun))
			}
		default:
			state := prState(s.PR.State)
			if s.PR.Draft {
				state += ", draft"
			}
			fmt.Fprintf(&b, "%s: #%d %s\n", strings.ToLower(noun), s.PR.Number, state)
			if ci := ciCounts(s.Checks); ci != "" {
				b.WriteString("ci: " + ci + "\n")
			} else if s.CheckErr != "" {
				b.WriteString("ci: lookup failed\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func worktreeFact(wt forge.Worktree) string {
	var tags []string
	switch {
	case wt.Bare:
		tags = append(tags, "bare")
	case wt.Branch != "":
		tags = append(tags, cleanFact(wt.Branch))
	default:
		tags = append(tags, "detached "+cleanFact(wt.Head))
	}
	for _, t := range []struct {
		on   bool
		name string
	}{{wt.Current, "current"}, {wt.Dirty, "dirty"}, {wt.Locked, "locked"}, {wt.Prunable, "prunable"}} {
		if t.on {
			tags = append(tags, t.name)
		}
	}
	return cleanFact(wt.Path) + " [" + strings.Join(tags, ", ") + "]"
}

// prState passes only the states a forge is known to report; anything else
// is "unknown" rather than forge-supplied text.
func prState(s string) string {
	switch s {
	case "open", "opened", "closed", "merged", "locked":
		return s
	}
	return "unknown"
}

func ciCounts(checks []forge.Check) string {
	counts := map[string]int{}
	for _, c := range checks {
		counts[c.State]++
	}
	var parts []string
	for _, st := range []string{forge.CheckPass, forge.CheckFail, forge.CheckPending, forge.CheckSkipped, forge.CheckCancel} {
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	return strings.Join(parts, ", ")
}

// cleanFact flattens a repository-local value (a path, a branch name) onto one
// line with delimiter markup removed.
func cleanFact(s string) string {
	return forge.Clean(sanitize.Sanitize(s))
}
