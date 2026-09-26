package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/tui/components"
)

// forgeState caches the last git/forge probe behind the runs panel's git and
// ci tabs. Every field is touched only on the Update goroutine; the probe
// itself runs in a tea.Cmd and lands as a forgeProbeMsg.
type forgeState struct {
	snap     forge.Snapshot
	have     bool
	dir      string // directory the snapshot was probed from
	probedAt time.Time
	inFlight bool
}

// forgeProbeMsg carries one finished probe.
type forgeProbeMsg struct {
	snap forge.Snapshot
	dir  string
	at   time.Time
}

const (
	// forgeTTL is how long a snapshot is fresh while a forge tab is showing.
	forgeTTL = 30 * time.Second
	// forgeProbeBudget bounds one whole probe, every CLI call included.
	forgeProbeBudget = 20 * time.Second
)

// forgeRun returns the runner forge calls go through; tests stub it.
func (a *App) forgeRun() forge.Runner {
	if a.forgeRunner != nil {
		return a.forgeRunner
	}
	return forge.ExecRunner
}

// forgeDir is the directory probes start from: the session's working
// directory, so a switch to another worktree is reflected.
func (a *App) forgeDir() string {
	if a.cwd != "" {
		return a.cwd
	}
	return a.workdir
}

// forgeTabActive reports whether a forge tab is on screen.
func (a *App) forgeTabActive() bool {
	return a.runsOpen && (a.runsTab == tabGit || a.runsTab == tabCI)
}

// ciTabVisible reports whether the ci tab is offered: the branch has a PR/MR
// on a forge whose CLI is installed.
func (a *App) ciTabVisible() bool {
	return a.forge.have && a.forge.snap.CIAvailable()
}

// startForgeCache creates the snapshot cache the agent session shares and
// starts its first background probe, so the first turn already carries the
// git and forge facts. Start calls it after the trust gate; a directory
// outside a repository is not probed.
func (a *App) startForgeCache() {
	if a.forgeCache == nil {
		a.forgeCache = &forge.Cache{Runner: a.forgeRunner, Look: a.forgeLook}
	}
	if a.gitOK {
		a.forgeCache.RefreshAsync(a.forgeDir(), forgeTTL, nil)
	}
}

// refreshForge starts a probe unless one is running or, without force, the
// snapshot is still fresh for the same directory. It never runs while the
// panel is closed. A fresher snapshot the shared cache already holds (the
// startup probe, or one a turn asked for) is adopted instead of re-probing.
func (a *App) refreshForge(force bool) tea.Cmd {
	if !a.runsOpen || a.forge.inFlight {
		return nil
	}
	dir := a.forgeDir()
	if snap, at, ok := a.forgeCache.Get(dir); ok && (!a.forge.have || a.forge.dir != dir || at.After(a.forge.probedAt)) {
		a.forge.snap, a.forge.dir, a.forge.probedAt, a.forge.have = snap, dir, at, true
	}
	if !force && a.forge.have && a.forge.dir == dir && time.Since(a.forge.probedAt) < forgeTTL {
		return nil
	}
	a.forge.inFlight = true
	r, look := a.forgeRun(), a.forgeLook
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), forgeProbeBudget)
		defer cancel()
		return forgeProbeMsg{snap: forge.Probe(ctx, r, look, dir), dir: dir, at: time.Now()}
	}
}

// handleForgeProbe applies a finished probe. When the ci tab was showing and
// no longer applies, the panel falls back to the git tab.
func (a *App) handleForgeProbe(m forgeProbeMsg) {
	a.forge.inFlight = false
	a.forge.snap = m.snap
	a.forge.dir = m.dir
	a.forge.probedAt = m.at
	a.forge.have = true
	a.forgeCache.Store(m.dir, m.snap, m.at)
	if a.runsTab == tabCI && !a.ciTabVisible() {
		a.runsTab = tabGit
		a.runsSel, a.runsScroll = 0, 0
	}
	if n := len(a.runsItems()); a.runsSel >= n {
		a.runsSel = max(n-1, 0)
	}
}

// runsTabNames lists the tab labels in index order; ci is last and is left
// off while it does not apply, so the indices of the others never move.
func (a *App) runsTabNames() []string {
	names := []string{"activity", "subagents", "processes", "git"}
	if a.ciTabVisible() {
		names = append(names, "ci")
	}
	return names
}

// nextRunsTab returns the tab after the current one, skipping a hidden ci tab.
func (a *App) nextRunsTab() int {
	next := (a.runsTab + 1) % tabCount
	if next == tabCI && !a.ciTabVisible() {
		next = tabActivity
	}
	return next
}

// gitItems lists the worktrees as runs rows.
func (a *App) gitItems() []runsItem {
	if !a.forge.have {
		return nil
	}
	s := a.forge.snap
	items := make([]runsItem, 0, len(s.Worktrees))
	for _, wt := range s.Worktrees {
		var parts []string
		switch {
		case wt.Bare:
			parts = append(parts, "bare")
		case wt.Branch != "":
			parts = append(parts, "⎇ "+wt.Branch)
		default:
			parts = append(parts, "detached @ "+wt.Head)
		}
		if wt.Current {
			parts = append(parts, "current")
		}
		if s.Main(wt) {
			parts = append(parts, "main worktree")
		}
		if wt.Dirty {
			parts = append(parts, "dirty")
		}
		if wt.Locked {
			parts = append(parts, "locked")
		}
		if wt.Prunable {
			parts = append(parts, "prunable")
		}
		state := ""
		switch {
		case wt.Current:
			state = "main"
		case wt.Prunable:
			state = "failed"
		}
		items = append(items, runsItem{ID: "wt:" + wt.Path, Label: displayPath(wt.Path), Detail: strings.Join(parts, " · "), State: state})
	}
	return items
}

// ciItems lists the CI checks as runs rows.
func (a *App) ciItems() []runsItem {
	if !a.ciTabVisible() {
		return nil
	}
	checks := a.forge.snap.Checks
	items := make([]runsItem, 0, len(checks))
	for i, c := range checks {
		detail := checkGlyph(c.State) + " " + c.State
		if c.Workflow != "" {
			detail = c.Workflow + " · " + detail
		}
		items = append(items, runsItem{ID: fmt.Sprintf("ci:%d", i), Label: c.Name, Detail: detail, State: checkRowState(c.State)})
	}
	return items
}

func checkGlyph(state string) string {
	switch state {
	case forge.CheckPass:
		return "✓"
	case forge.CheckFail:
		return "✗"
	case forge.CheckSkipped, forge.CheckCancel:
		return "–"
	}
	return "…"
}

// checkRowState maps a check onto the runs row colours.
func checkRowState(state string) string {
	switch state {
	case forge.CheckPass:
		return "done"
	case forge.CheckFail, forge.CheckCancel:
		return "failed"
	case forge.CheckPending:
		return "running"
	}
	return ""
}

// runsSummary returns the harness-composed lines the git and ci tabs show
// above their rows. Every value in them was cleaned by internal/forge.
func (a *App) runsSummary() []string {
	switch a.runsTab {
	case tabGit:
		return a.gitSummary()
	case tabCI:
		return a.ciSummary()
	}
	return nil
}

func (a *App) gitSummary() []string {
	if !a.forge.have {
		return nil
	}
	s := a.forge.snap
	if s.Root == "" {
		return nil
	}
	branch := s.Branch
	if branch == "" {
		branch = "(detached)"
	}
	line := "⎇ " + branch
	if s.Upstream != "" {
		line += " → " + s.Upstream
	} else if s.Branch != "" {
		line += " (no upstream)"
	}
	if s.Provider != nil {
		line += " · " + s.Provider.Label()
	}
	if s.Remote.URL != "" {
		line += " · " + s.Remote.URL
	}
	line += a.forgeStaleMark()
	lines := []string{line}

	var pr string
	switch {
	case s.Provider == nil:
		pr = s.Reason
	case s.PRErr != "":
		pr = s.PRErr
	case s.PR != nil:
		pr = fmt.Sprintf("%s #%d %s (%s", s.Provider.Noun(), s.PR.Number, s.PR.Title, s.PR.State)
		if s.PR.Draft {
			pr += ", draft"
		}
		pr += ")"
	default:
		pr = "no " + s.Provider.Noun() + " for this branch · p create"
	}
	if pr != "" {
		lines = append(lines, pr)
	}
	if s.WorktreeErr != "" {
		lines = append(lines, "worktrees: "+s.WorktreeErr)
	}
	return lines
}

func (a *App) ciSummary() []string {
	if !a.ciTabVisible() {
		return nil
	}
	s := a.forge.snap
	counts := map[string]int{}
	for _, c := range s.Checks {
		counts[c.State]++
	}
	line := fmt.Sprintf("%s #%d", s.Provider.Noun(), s.PR.Number)
	for _, st := range []string{forge.CheckPass, forge.CheckFail, forge.CheckPending, forge.CheckSkipped, forge.CheckCancel} {
		if n := counts[st]; n > 0 {
			line += fmt.Sprintf(" · %d %s", n, st)
		}
	}
	line += a.forgeStaleMark()
	lines := []string{line}
	if s.CheckErr != "" {
		lines = append(lines, s.CheckErr)
	}
	return lines
}

// forgeStaleMark is " (probing…)" during a probe and " (stale · 45s)" once
// the snapshot is older than its TTL.
func (a *App) forgeStaleMark() string {
	if a.forge.inFlight {
		return " (probing…)"
	}
	if age := time.Since(a.forge.probedAt); age >= forgeTTL {
		return fmt.Sprintf(" (stale · %s)", age.Truncate(time.Second))
	}
	return ""
}

// forgePlaceholder is the row shown when a forge tab has no items.
func (a *App) forgePlaceholder() string {
	s := a.forge.snap
	switch {
	case !a.forge.have:
		return "reading git state…"
	case a.runsTab == tabCI && s.CheckErr == "":
		return "no checks reported"
	case a.runsTab == tabCI:
		return ""
	case s.Root == "":
		return "not a git repository"
	}
	return "no worktrees"
}

// renderSummaryLine styles one summary line.
func renderSummaryLine(s string) string {
	return components.MutedStyle.Render("  " + s)
}

// displayPath abbreviates the home directory to ~.
func displayPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}
