package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/tui/components"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

type codeReviewConfigState struct {
	cap      vulnetixcli.Capabilities
	loading  bool
	errorMsg string
}

type codeReviewListState struct {
	entries   []projectregistry.Entry
	rows      []codeReviewListRow
	cursor    int
	scroll    int
	filtering bool
	filter    string
	loading   bool
	errorMsg  string
}

type codeReviewListRow struct {
	entry   projectregistry.Entry
	label   string
	details string
}

type codeReviewArtifactsState struct {
	summary  scanartifacts.Summary
	cursor   int
	scroll   int
	loading  bool
	errorMsg string
}

// ---------------------------------------------------------------------------
// Async commands
// ---------------------------------------------------------------------------

func (a *App) probeVulnetixCmd() tea.Cmd {
	return func() tea.Msg {
		cli, err := vulnetixcli.Detect()
		if err != nil {
			return vulnetixProbeMsg{err: err}
		}
		cap := vulnetixcli.Probe(context.Background(), *cli, vulnetixcli.ProbeOptions{
			Client:      a.client,
			SkipNetwork: false,
			Observer:    a,
		})
		return vulnetixProbeMsg{cap: cap}
	}
}

func (a *App) loadCodeReviewProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		reg, err := projectregistry.Load()
		if err != nil {
			return projectsLoadedMsg{err: err}
		}
		return projectsLoadedMsg{entries: reg.All()}
	}
}

func (a *App) sweepProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		logd, _ := config.GlobalDir()
		reg, _ := projectregistry.Load()
		if !a.settings.SweepEnabled() {
			return sweepFoundMsg{found: 0}
		}
		if !reg.LastSweep().IsZero() && time.Since(reg.LastSweep()) < 24*time.Hour {
			return sweepFoundMsg{found: 0}
		}

		roots := a.settings.SweepRoots()
		if len(roots) == 0 {
			home, _ := os.UserHomeDir()
			parent := filepath.Dir(a.workdir)
			roots = []string{home, parent}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out := make(chan projectregistry.Found, 100)
		go projectregistry.Sweep(ctx, projectregistry.SweepOptions{Roots: roots, MaxDepth: 6, Budget: 20 * time.Second}, out)

		found := 0
		for f := range out {
			found++
			_ = projectregistry.Observe(f.Path, projectregistry.SourceSweep)
		}
		_ = projectregistry.Mutate(func(r *projectregistry.Registry) error {
			r.SetLastSweep(time.Now())
			return nil
		})
		_ = logd
		return sweepFoundMsg{found: found}
	}
}

func (a *App) loadArtifactsCmd(workdir string) tea.Cmd {
	return func() tea.Msg {
		summary, err := scanartifacts.Refresh(context.Background(), workdir)
		return artifactsLoadedMsg{summary: summary, err: err}
	}
}

// ---------------------------------------------------------------------------
// Config view
// ---------------------------------------------------------------------------

func (a *App) enterCodeReviewConfig() tea.Cmd {
	a.codeReviewConfigState = codeReviewConfigState{loading: true}
	return a.probeVulnetixCmd()
}

func (a *App) codeReviewConfigView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("code review", "configure", w))
	if a.codeReviewConfigState.loading {
		b.WriteString(components.AccentStyle.Render("  ○ Probing Vulnetix CLI…") + "\n")
		return b.String()
	}
	cap := a.codeReviewConfigState.cap
	if !cap.Present {
		b.WriteString(components.DangerStyle.Render("  ✗ Vulnetix CLI not found") + "\n")
		b.WriteString(components.MutedStyle.Render("  install it to enable /code-review"))
		return b.String()
	}
	b.WriteString(renderLabelValue("Path", cap.Path, w))
	b.WriteString(renderLabelValue("Resolved", cap.RealPath, w))
	b.WriteString(renderLabelValue("Version", cap.Version.String(), w))
	b.WriteString(renderLabelValue("Install", string(cap.Install), w))
	b.WriteString(renderLabelValue("Auth", authLabel(cap.Auth), w))
	b.WriteString(renderLabelValue("Plan", string(cap.Auth.Plan), w))
	b.WriteString(renderLabelValue("Org ID", cap.Auth.OrgID, w))
	b.WriteString(renderLabelValue("API", fmt.Sprintf("reachable=%v", cap.API.Reachable), w))
	if a.codeReviewConfigState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.codeReviewConfigState.errorMsg) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("r", "re-probe", "l", "history", "esc", "back") + "\n")
	return b.String()
}

func authLabel(a vulnetixcli.AuthState) string {
	if !a.Parsed {
		return "—"
	}
	if a.Authenticated {
		return "authenticated"
	}
	return "unauthenticated"
}

func renderLabelValue(label, value string, width int) string {
	if value == "" {
		value = "—"
	}
	line := fmt.Sprintf("  %-12s %s", label+":", components.EmphStyle.Render(value))
	return truncateLine(line, width) + "\n"
}

func (a *App) handleCodeReviewConfigKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "r":
		a.codeReviewConfigState.loading = true
		return a, a.probeVulnetixCmd()
	case "l":
		return a, a.push(viewCodeReviewList)
	}
	return a, nil
}

// ---------------------------------------------------------------------------
// List view
// ---------------------------------------------------------------------------

func flattenCodeReviewRows(entries []projectregistry.Entry) []codeReviewListRow {
	rows := make([]codeReviewListRow, 0, len(entries))
	for _, e := range entries {
		label := e.Name
		if label == "" {
			label = filepath.Base(e.Path)
		}
		details := fmt.Sprintf("%d artifacts · %s", 0, ageLabel(e.LastSeen.UnixMilli()))
		rows = append(rows, codeReviewListRow{entry: e, label: label, details: details})
	}
	return rows
}

func (a *App) enterCodeReviewList() tea.Cmd {
	a.codeReviewListState = codeReviewListState{loading: true}
	return tea.Batch(a.loadCodeReviewProjectsCmd(), a.sweepProjectsCmd())
}

func (a *App) codeReviewListView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("code review", "history", w))
	if a.codeReviewListState.loading {
		b.WriteString(components.AccentStyle.Render("  ○ Loading projects…") + "\n")
		return b.String()
	}
	rows := filterCodeReviewRows(a.codeReviewListState.rows, a.codeReviewListState.filter)
	if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("  no projects registered") + "\n")
	}
	nrows := 10
	if a.height > 0 {
		nrows = a.height - lipgloss.Height(b.String()) - 4
	}
	if nrows < 3 {
		nrows = 3
	}
	a.codeReviewListState.scroll = windowStart(a.codeReviewListState.scroll, a.codeReviewListState.cursor, len(rows), nrows)
	start := a.codeReviewListState.scroll
	end := min(start+nrows, len(rows))
	for i := start; i < end; i++ {
		r := rows[i]
		name := r.label
		if i == a.codeReviewListState.cursor {
			name = components.EmphStyle.Render(name)
		}
		line := components.Cursor(i == a.codeReviewListState.cursor) + name
		line += "  " + components.MutedStyle.Render(r.details)
		b.WriteString(line + "\n")
	}
	if a.codeReviewListState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.codeReviewListState.errorMsg) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "artifacts", "r", "sweep", "c", "configure", "esc", "back") + "\n")
	return b.String()
}

func filterCodeReviewRows(rows []codeReviewListRow, q string) []codeReviewListRow {
	if q == "" {
		return rows
	}
	lower := strings.ToLower(q)
	var out []codeReviewListRow
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.label), lower) || strings.Contains(strings.ToLower(r.entry.Path), lower) {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) handleCodeReviewListKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.codeReviewListState.filtering {
		return a.handleCodeReviewListFilterKey(m)
	}
	rows := filterCodeReviewRows(a.codeReviewListState.rows, a.codeReviewListState.filter)
	switch m.String() {
	case "esc":
		if a.codeReviewListState.filter != "" {
			a.codeReviewListState.filter = ""
			a.codeReviewListState.scroll = 0
			return a, nil
		}
		a.pop()
		return a, nil
	case "up", "k":
		a.codeReviewListState.cursor = max(a.codeReviewListState.cursor-1, 0)
		return a, nil
	case "down", "j":
		a.codeReviewListState.cursor = min(a.codeReviewListState.cursor+1, len(rows)-1)
		return a, nil
	case "/":
		a.codeReviewListState.filtering = true
		return a, nil
	case "r":
		a.codeReviewListState.loading = true
		return a, tea.Batch(a.loadCodeReviewProjectsCmd(), a.sweepProjectsCmd())
	case "c":
		return a, a.push(viewCodeReviewConfig)
	case "enter":
		if len(rows) == 0 || a.codeReviewListState.cursor >= len(rows) {
			return a, nil
		}
		workdir := rows[a.codeReviewListState.cursor].entry.Path
		return a, a.loadArtifactsCmd(workdir)
	}
	return a, nil
}

func (a *App) handleCodeReviewListFilterKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		a.codeReviewListState.filtering = false
		a.codeReviewListState.filter = ""
		a.codeReviewListState.scroll = 0
		return a, nil
	case tea.KeyEnter:
		a.codeReviewListState.filtering = false
		return a, nil
	case tea.KeyRunes:
		a.codeReviewListState.filter += string(m.Runes)
		a.codeReviewListState.scroll = 0
		return a, nil
	case tea.KeySpace:
		a.codeReviewListState.filter += " "
		a.codeReviewListState.scroll = 0
		return a, nil
	case tea.KeyBackspace:
		a.codeReviewListState.filter = trimLastRune(a.codeReviewListState.filter)
		a.codeReviewListState.scroll = 0
		return a, nil
	}
	return a, nil
}

// ---------------------------------------------------------------------------
// Artifacts view
// ---------------------------------------------------------------------------

func (a *App) codeReviewArtifactsView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("code review", "artifacts", w))
	if a.codeReviewArtifactsState.loading {
		b.WriteString(components.AccentStyle.Render("  ○ Loading artifacts…") + "\n")
		return b.String()
	}
	s := a.codeReviewArtifactsState.summary
	if s.Dir == "" {
		b.WriteString(components.MutedStyle.Render("  no artifact summary loaded") + "\n")
		return b.String()
	}
	b.WriteString(renderLabelValue("Project", filepath.Base(s.Dir), w))
	b.WriteString(renderLabelValue("Findings", s.Union.Format(), w))
	b.WriteString("\n")
	arts := visibleArtifacts(s.Artifacts)
	nrows := 10
	if a.height > 0 {
		nrows = a.height - lipgloss.Height(b.String()) - 4
	}
	if nrows < 3 {
		nrows = 3
	}
	a.codeReviewArtifactsState.scroll = windowStart(a.codeReviewArtifactsState.scroll, a.codeReviewArtifactsState.cursor, len(arts), nrows)
	start := a.codeReviewArtifactsState.scroll
	end := min(start+nrows, len(arts))
	for i := start; i < end; i++ {
		art := arts[i]
		name := art.Rel
		if i == a.codeReviewArtifactsState.cursor {
			name = components.EmphStyle.Render(name)
		}
		line := components.Cursor(i == a.codeReviewArtifactsState.cursor) + name
		if art.Superseded {
			line += components.MutedStyle.Render(" (superseded)")
		}
		if fs, ok := s.PerFile[art.Rel]; ok && fs.SkipReason != "" {
			line += "  " + components.WarnStyle.Render(fs.SkipReason)
		}
		line += "  " + components.MutedStyle.Render(art.Description)
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "t", "triage", "l", "history", "esc", "back") + "\n")
	return b.String()
}

func visibleArtifacts(arts []scanartifacts.Artifact) []scanartifacts.Artifact {
	var out []scanartifacts.Artifact
	for _, a := range arts {
		if a.Kind == scanartifacts.KindSignet {
			continue
		}
		out = append(out, a)
	}
	// Newest first by timestamp/mtime.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Stamp.IsZero() && !out[j].Stamp.IsZero() {
			return out[i].Stamp.After(out[j].Stamp)
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out
}

func (a *App) handleCodeReviewArtifactsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	arts := visibleArtifacts(a.codeReviewArtifactsState.summary.Artifacts)
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		a.codeReviewArtifactsState.cursor = max(a.codeReviewArtifactsState.cursor-1, 0)
		return a, nil
	case "down", "j":
		a.codeReviewArtifactsState.cursor = min(a.codeReviewArtifactsState.cursor+1, len(arts)-1)
		return a, nil
	case "l":
		return a, a.push(viewCodeReviewList)
	case "t":
		if a.bgManager == nil {
			a.addSystem("triage: no background manager")
			return a, nil
		}
		projectRoot := filepath.Dir(a.codeReviewArtifactsState.summary.Dir)
		return a, a.startTriage(projectRoot)
	}
	return a, nil
}

func truncateLine(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return s[:max(width-1, 0)] + "…"
}
