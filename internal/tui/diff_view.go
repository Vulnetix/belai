package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/workdiff"
)

// The /diff pane is the user's own read-only view of the working tree's git
// changes (docs/diff.md). Its text goes to the screen and nowhere else: not a
// model, the transcript, telemetry, audit or sync. A path a Read deny rule
// covers is listed by name and status, and its content is never read.

// diffCollectBudget bounds one collection.
const diffCollectBudget = 20 * time.Second

// diffListRows is the most file rows the list shows at once.
const diffListRows = 6

type diffViewState struct {
	gen      int
	loading  bool
	snap     workdiff.Snapshot
	selected int
	scroll   int

	// lines is the selected file's rendered body, cached by file and width.
	lines     []string
	linesFor  int
	linesWide int
}

// workdiffLoadedMsg carries a collection back to the UI goroutine.
type workdiffLoadedMsg struct {
	gen  int
	snap workdiff.Snapshot
}

func (a *App) enterDiff() tea.Cmd {
	prev := a.diffState
	a.diffState = diffViewState{gen: prev.gen + 1, loading: true, linesFor: -1}
	return a.loadDiffCmd(a.diffState.gen)
}

// loadDiffCmd collects the changes off the UI goroutine. The hide function is
// built here, on the UI goroutine, from a snapshot of the permission rules.
func (a *App) loadDiffCmd(gen int) tea.Cmd {
	workdir := a.workdir
	hide := a.diffHide()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffCollectBudget)
		defer cancel()
		return workdiffLoadedMsg{gen: gen, snap: workdiff.Collect(ctx, workdir, hide)}
	}
}

// diffHide is the conservative read policy: a path a Read rule denies is
// shown by name only. An ask rule does not hide it, since the user is the one
// looking.
func (a *App) diffHide() func(rel string) bool {
	perms := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
	workdir := a.workdir
	return func(rel string) bool {
		return diffReadDenied(perms, workdir, rel)
	}
}

// diffReadDenied reports whether a Read deny rule covers rel (a path relative
// to the repository root, slash separated). The rule is tried against the
// relative and the absolute path, with the workdir as both anchor candidates.
func diffReadDenied(perms permissions.Settings, workdir, rel string) bool {
	subjects := []string{rel, filepath.Join(workdir, filepath.FromSlash(rel))}
	for _, s := range subjects {
		if dec, rule := perms.Explain("Read", s); rule != "" && dec == permissions.DecisionBlock {
			return true
		}
	}
	return false
}

func (a *App) handleDiffMsg(msg tea.Msg) (tea.Cmd, bool) {
	m, ok := msg.(workdiffLoadedMsg)
	if !ok {
		return nil, false
	}
	st := &a.diffState
	if m.gen != st.gen {
		return nil, true
	}
	st.loading, st.snap, st.linesFor = false, m.snap, -1
	if st.selected >= len(st.snap.Files) {
		st.selected = max(len(st.snap.Files)-1, 0)
	}
	st.scroll = 0
	return nil, true
}

// diffChrome is what the screen spends outside the body: the view padding (2),
// the header and rule (2), the summary line, the rule under the list and the
// key line.
const diffChrome = 7

func (a *App) diffListHeight() int {
	n := len(a.diffState.snap.Files)
	if a.height > 0 && a.height < 24 {
		return min(n, 3)
	}
	return min(n, diffListRows)
}

func (a *App) diffBodyHeight() int {
	if a.height <= 0 {
		return 20
	}
	return max(a.height-diffChrome-a.diffListHeight(), 3)
}

// diffBody renders the selected file, cached per file and width.
func (a *App) diffBody(w int) []string {
	st := &a.diffState
	if len(st.snap.Files) == 0 {
		return nil
	}
	if st.linesFor == st.selected && st.linesWide == w {
		return st.lines
	}
	f := st.snap.Files[st.selected]
	var lines []string
	switch {
	case f.Hidden, f.Note != "" && len(f.Change.Old)+len(f.Change.New) == 0 && !f.Change.Binary:
		lines = []string{components.MutedStyle.Render("  " + f.Note)}
	default:
		ch := filediff.Change{Files: []filediff.FileChange{f.Change}}
		out := components.DiffView(&ch, w)
		lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
		if out == "" {
			lines = []string{components.MutedStyle.Render("  no text diff")}
		}
	}
	st.lines, st.linesFor, st.linesWide = lines, st.selected, w
	return lines
}

func diffStatusGlyph(f workdiff.File) string {
	switch {
	case f.Hidden:
		return "◌"
	case f.Conflict:
		return "!"
	case f.Untracked:
		return "?"
	case f.Staged && f.Unstaged:
		return "±"
	case f.Staged:
		return "S"
	}
	return "M"
}

func (a *App) diffView() string {
	w := a.contentWidth()
	st := &a.diffState
	var b strings.Builder
	b.WriteString(components.SectionHeader("Changes", "git working tree · read only · esc back", w))

	switch {
	case st.loading:
		b.WriteString(components.MutedStyle.Render("reading the working tree…"))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	case st.snap.Unavailable != "":
		b.WriteString(components.MutedStyle.Render(st.snap.Unavailable) + "\n")
		b.WriteString("\n" + components.HelpBar("r", "retry", "esc", "back"))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	case len(st.snap.Files) == 0:
		b.WriteString(components.MutedStyle.Render("the working tree is clean: no staged, unstaged or untracked changes") + "\n")
		b.WriteString("\n" + components.HelpBar("r", "reload", "esc", "back"))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	s := st.snap
	sum := fmt.Sprintf("%d files · %d staged · %d unstaged · %d untracked · ", len(s.Files)+s.More, s.Staged, s.Unstaged, s.Untracked)
	b.WriteString(components.MutedStyle.Render(sum) +
		lipgloss.NewStyle().Foreground(components.ColorTealSoft).Render(fmt.Sprintf("+%d", s.Added)) + " " +
		lipgloss.NewStyle().Foreground(components.ColorDanger).Render(fmt.Sprintf("−%d", s.Removed)))
	if s.More > 0 {
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf(" (first %d listed)", len(s.Files))))
	}
	b.WriteString("\n")

	// The list window follows the selection.
	lh := a.diffListHeight()
	top := 0
	if st.selected >= lh {
		top = st.selected - lh + 1
	}
	for i := top; i < top+lh && i < len(s.Files); i++ {
		f := s.Files[i]
		stat := ""
		if !f.Hidden && !f.Change.Binary {
			stat = fmt.Sprintf(" +%d −%d", f.Added, f.Removed)
		}
		line := fmt.Sprintf("%s %s%s", diffStatusGlyph(f), f.Path, stat)
		line = ansi.Truncate(line, max(w-2, 10), "…")
		if i == st.selected {
			b.WriteString(components.EmphStyle.Render("▸ " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}
	b.WriteString(components.Rule(w) + "\n")

	body := a.diffBody(w)
	h := a.diffBodyHeight()
	maxScroll := max(len(body)-h, 0)
	st.scroll = min(max(st.scroll, 0), maxScroll)
	end := min(st.scroll+h, len(body))
	b.WriteString(strings.Join(body[st.scroll:end], "\n"))
	b.WriteString("\n")

	pos := ""
	if len(body) > h {
		pos = fmt.Sprintf("%d–%d/%d", st.scroll+1, end, len(body))
	}
	b.WriteString(glHelpLine([]glKey{
		{"←→", "file", 1}, {"↑↓", "scroll", 1}, {"esc", "back", 1},
		{"pgup/pgdn", "page", 2}, {"g/G", "ends", 3}, {"r", "reload", 2},
	}, max(w-len(pos)-2, 20)))
	if pos != "" {
		b.WriteString("  " + components.MutedStyle.Render(pos))
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleDiffKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.diffState
	n := len(st.snap.Files)
	page := max(a.diffBodyHeight()-1, 1)
	scrollTo := func(v int) { st.scroll = max(v, 0) } // clamped in the render
	selectFile := func(i int) {
		if n == 0 {
			return
		}
		i = (i%n + n) % n
		if i != st.selected {
			st.selected, st.scroll = i, 0
		}
	}
	switch m.String() {
	case "esc", "q":
		a.pop()
	case "right", "n", "tab", "l":
		selectFile(st.selected + 1)
	case "left", "p", "shift+tab", "h":
		selectFile(st.selected - 1)
	case "down", "j":
		scrollTo(st.scroll + 1)
	case "up", "k":
		scrollTo(st.scroll - 1)
	case "pgdown", "ctrl+d", " ":
		scrollTo(st.scroll + page)
	case "pgup", "ctrl+u":
		scrollTo(st.scroll - page)
	case "home", "g":
		scrollTo(0)
	case "end", "G":
		scrollTo(1 << 30)
	case "r":
		st.gen++
		st.loading = true
		return a, a.loadDiffCmd(st.gen)
	}
	return a, nil
}
