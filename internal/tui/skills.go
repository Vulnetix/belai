package tui

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/skills"
)

// skillsListing renders /skills: one line per installed skill with its
// source. Descriptions come from skill files (possibly a plugin's), so they
// are sanitized and flattened to one line.
func skillsListing(entries []skills.Entry) string {
	if len(entries) == 0 {
		return "no skills installed · add one under ~/.vulnetix/belai/skills/<name>/SKILL.md, or let the agent draft one with SkillDraft"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d skill(s) installed:", len(entries))
	for _, e := range entries {
		desc := strings.Join(strings.Fields(sanitize.Sanitize(e.Description)), " ")
		if r := []rune(desc); len(r) > 100 {
			desc = string(r[:100]) + "…"
		}
		note := ""
		if e.DisableModelInvocation {
			note = " (user only)"
		}
		fmt.Fprintf(&b, "\n  %s [%s]%s: %s", e.Name, e.Source, note, desc)
	}
	return b.String()
}
