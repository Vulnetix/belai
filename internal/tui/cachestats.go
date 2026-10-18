package tui

import (
	"fmt"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// cacheStats sums how much of this session's agent prompts the provider
// served from its prompt cache. A healthy loop reads nearly the whole prompt
// from cache on every call after the first and writes only the last turn; a
// low read share is the signal that something rewrote the prompt prefix.
type cacheStats struct {
	calls  int
	prompt int
	read   int
	write  int
	// last is the most recent agent call's figures.
	last run.UsageEvent
}

// add folds one call in. Only agent calls count: role-manager calls are
// short, often below a provider's cacheable minimum, and would dilute the
// share that says whether the conversation prefix is stable.
func (c *cacheStats) add(ev run.UsageEvent) {
	if ev.Role != run.RoleAgent || ev.Prompt <= 0 {
		return
	}
	c.calls++
	c.prompt += ev.Prompt
	c.read += ev.CacheRead
	c.write += ev.CacheWrite
	c.last = ev
}

// reported says whether the provider reported any cache activity at all.
func (c cacheStats) reported() bool { return c.read+c.write > 0 }

// cachePct is part as a whole-number percentage of whole.
func cachePct(part, whole int) int {
	if whole <= 0 {
		return 0
	}
	return part * 100 / whole
}

// line renders the cache summary for the session intelligence screen.
func (c cacheStats) line(provider string) string {
	switch {
	case c.calls == 0:
		return components.LowStyle.Render("  prompt cache · nothing sent yet")
	case !c.reported():
		who := provider
		if who == "" {
			who = "this provider"
		}
		return components.LowStyle.Render("  prompt cache · not reported by " + who)
	}
	l := c.last
	return components.MutedStyle.Render(fmt.Sprintf(
		"  prompt cache · last call %d%% read (%s of %s, %s written) · session %d%% read over %d calls",
		cachePct(l.CacheRead, l.Prompt), humanTokens(int64(l.CacheRead)), humanTokens(int64(l.Prompt)),
		humanTokens(int64(l.CacheWrite)), cachePct(c.read, c.prompt), c.calls))
}
