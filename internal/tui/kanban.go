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

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The global kanban board in the TUI: the store every session's tools share,
// the sync worker that mirrors it to the Vulnetix website, the pane above an
// idle empty composer, and /kanban (kanban_view.go).

// kanbanUI is the TUI's board state. nil means the kanban setting is off.
type kanbanUI struct {
	store  *kanban.Store
	src    *kanban.Source
	syncer *kanban.Syncer
	stop   context.CancelFunc
	// syncNote says why sync is not running, for /kanban.
	syncNote string

	pane kanbanPaneState
	view kanbanViewState
}

// kanbanPaneState is the composer pane's filter and focus.
type kanbanPaneState struct {
	focus  bool
	sel    int
	filter int  // index into kanbanPaneFilters
	all    bool // every project rather than this one
	query  string
}

// kanbanPaneFilters are the pane's list filters, in tab order.
var kanbanPaneFilters = []struct {
	label string
	lists []kanban.List
}{
	{"all", []kanban.List{kanban.Review, kanban.Blocked, kanban.Backlog}},
	{"backlog", []kanban.List{kanban.Backlog}},
	{"review", []kanban.List{kanban.Review}},
	{"blocked", []kanban.List{kanban.Blocked}},
}

// kanbanPaneRows is how many items the pane lists.
const kanbanPaneRows = 5

// kanbanListColor is each list's colour, shared by the pane and /kanban.
func kanbanListColor(l kanban.List) lipgloss.TerminalColor {
	switch l {
	case kanban.Review:
		return components.ColorAmber
	case kanban.Blocked:
		return components.ColorDanger
	case kanban.InProgress:
		return components.ColorTeal
	case kanban.Done:
		return components.ColorTealSoft
	}
	return components.ColorMuted
}

func kanbanChip(l kanban.List) string {
	return components.Chip(strings.ReplaceAll(string(l), "_", " "), kanbanListColor(l))
}

// initKanban opens the board when the setting is on. It does no I/O: the
// store reads the file on first use.
func (a *App) initKanban() {
	if !a.settings.KanbanEnabled() {
		a.kb = nil
		return
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		a.kb = nil
		return
	}
	a.kb = &kanbanUI{store: store, src: kanban.NewSource(kanban.ProvenanceFor(a.workdir, a.sessionID, ""))}
}

// kanbanStore returns the board, or nil when the setting is off.
func (a *App) kanbanStore() (*kanban.Store, *kanban.Source) {
	if a.kb == nil {
		return nil, nil
	}
	return a.kb.store, a.kb.src
}

// startKanbanSync mirrors the board through the session-sync client, which
// already enforces the Vulnetix origin allowlist and credential handling.
// startSessionSync calls it once it has a client and a host id.
func (a *App) startKanbanSync(client *sessionsync.Client, hostID string) {
	if a.kb == nil || a.kb.syncer != nil {
		return
	}
	p := a.kb.src.Get()
	p.HostID = hostID
	a.kb.src.Set(p)
	ctx, cancel := context.WithCancel(context.Background())
	a.kb.stop = cancel
	a.kb.syncer = kanban.NewSyncer(a.kb.store, client, kanban.SyncOptions{})
	a.kb.syncer.Start(ctx)
	a.kb.syncNote = ""
}

// stopKanbanSync ends the worker after one last push.
func (a *App) stopKanbanSync() {
	if a.kb == nil || a.kb.syncer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = a.kb.syncer.Flush(ctx)
	cancel()
	a.kb.stop()
	a.kb.syncer, a.kb.stop = nil, nil
}

// kanbanSyncLabel is the one-line sync status.
func (a *App) kanbanSyncLabel() string {
	if a.kb == nil {
		return "off"
	}
	if a.kb.syncer == nil {
		if a.kb.syncNote != "" {
			return "sync " + a.kb.syncNote
		}
		return "local only (sync starts with a Vulnetix CLI login)"
	}
	st := a.kb.syncer.Status()
	switch {
	case st.LastError != "":
		return fmt.Sprintf("sync error: %s (%d pending)", st.LastError, st.Pending)
	case st.LastPull.IsZero():
		return "syncing…"
	}
	s := "synced " + humanSince(st.LastPull)
	if st.Pending > 0 {
		s += fmt.Sprintf(" · %d pending", st.Pending)
	}
	return s
}

func humanSince(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return t.Format("15:04")
}

// kanbanProject is this session's project filter value.
func (a *App) kanbanProject() string {
	p := a.kb.src.Get()
	if p.ProjectKey != "" {
		return p.ProjectKey
	}
	return p.Project
}

// kanbanPaneItems returns the pane's rows and whether they are every
// project's (the pane falls back to every project when this one has none).
func (a *App) kanbanPaneItems() ([]kanban.Item, bool) {
	if a.kb == nil {
		return nil, false
	}
	ps := a.kb.pane
	q := kanban.Query{Text: ps.query, Lists: kanbanPaneFilters[ps.filter].lists, Limit: 200}
	all := ps.all
	if !all {
		q.Project = a.kanbanProject()
	}
	items, err := a.kb.store.Search(q)
	if err != nil {
		return nil, all
	}
	if len(items) == 0 && !all && ps.query == "" && ps.filter == 0 {
		q.Project = ""
		items, _ = a.kb.store.Search(q)
		all = true
	}
	return items, all
}

// kanbanPaneVisible reports whether the pane shows: an idle chat with an
// empty composer and nothing else above it, and open items to show.
func (a *App) kanbanPaneVisible() bool {
	if a.kb == nil || !a.settings.KanbanEnabled() || a.view != viewChat || a.phase != phaseIdle || a.preSend || a.working() {
		return false
	}
	if !a.kb.pane.focus && (strings.TrimSpace(a.editor.Value()) != "" || a.editor.Masked) {
		return false
	}
	if len(a.autocomplete) > 0 || a.agentPickerVisible() || a.promptPickerVisible() || a.dirPickVisible() ||
		a.filePickerVisible() || a.rootConfirmVisible() || len(a.attachments) > 0 || a.historyActive ||
		a.savePromptMode || a.saveFileMode || a.promptAction || a.forgeFlowActive() || a.runsFocus || a.reviewActive() {
		return false
	}
	if a.kb.pane.focus {
		return true
	}
	items, _ := a.kanbanPaneItems()
	return len(items) > 0
}

// kanbanPaneHeight is the pane's rendered height, part of the chrome.
func (a *App) kanbanPaneHeight() int {
	if !a.kanbanPaneVisible() {
		return 0
	}
	return lipgloss.Height(a.renderKanbanPane())
}

// renderKanbanPane draws the pane.
func (a *App) renderKanbanPane() string {
	items, all := a.kanbanPaneItems()
	ps := &a.kb.pane
	if ps.sel >= len(items) {
		ps.sel = max(0, len(items)-1)
	}
	w := a.contentWidth()
	var chips []string
	for i, f := range kanbanPaneFilters {
		if i == ps.filter {
			chips = append(chips, components.Chip(f.label, components.ColorTealSoft))
		} else {
			chips = append(chips, components.MutedStyle.Render(f.label))
		}
	}
	scope := "this project"
	if all {
		scope = "all projects"
	}
	head := components.MutedStyle.Render("▤ kanban  ") + strings.Join(chips, " ") + components.MutedStyle.Render("  · "+scope)
	if ps.query != "" {
		head += components.MutedStyle.Render("  · /") + ps.query
	}
	if ps.focus {
		head += components.MutedStyle.Render("   ↑↓ select · tab list · p scope · type filter · ⏎ use · esc back")
	} else {
		head += components.MutedStyle.Render("   ↓ browse")
	}
	lines := []string{ansi.Truncate(head, w, "…")}
	start := 0
	if ps.sel >= kanbanPaneRows {
		start = ps.sel - kanbanPaneRows + 1
	}
	end := min(len(items), start+kanbanPaneRows)
	for i := start; i < end; i++ {
		it := items[i]
		prefix := "  "
		if ps.focus && i == ps.sel {
			prefix = components.AccentStyle.Render("▸ ")
		}
		row := prefix + kanbanChip(it.List) + " " + components.MutedStyle.Render(it.Short()) + " "
		if all {
			row += components.MutedStyle.Render(it.Project + " · ")
		}
		if !a.kanbanDirReachable(it.Dir) {
			row += components.WarnStyle.Render("⤴ ")
		}
		title := it.Title
		if ps.focus && i == ps.sel {
			title = components.EmphStyle.Render(title)
		}
		lines = append(lines, ansi.Truncate(row+title, w, "…"))
	}
	if len(items) == 0 {
		lines = append(lines, components.MutedStyle.Render("  nothing matches"))
	} else if more := len(items) - end; more > 0 {
		lines = append(lines, components.MutedStyle.Render(fmt.Sprintf("  +%d more", more)))
	}
	return strings.Join(lines, "\n")
}

// kanbanDirReachable reports whether dir is under the session's roots, so a
// prefilled prompt about it can be worked on here.
func (a *App) kanbanDirReachable(dir string) bool {
	if dir == "" {
		return true
	}
	roots := append([]string{a.workdir}, a.workspaceDirs...)
	for _, r := range roots {
		rel, err := filepath.Rel(r, dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	// A checkout's subdirectory session still owns the repository root.
	if rel, err := filepath.Rel(dir, a.workdir); err == nil && !strings.HasPrefix(rel, "..") {
		return true
	}
	return false
}

// focusKanbanPane is down on an empty composer while the pane shows.
func (a *App) focusKanbanPane() bool {
	if !a.kanbanPaneVisible() {
		return false
	}
	a.kb.pane.focus = true
	a.kb.pane.sel = 0
	return true
}

// handleKanbanPaneKey owns the keyboard while the pane is focused.
func (a *App) handleKanbanPaneKey(m tea.KeyMsg) tea.Cmd {
	ps := &a.kb.pane
	items, _ := a.kanbanPaneItems()
	switch m.String() {
	case "esc":
		if ps.query != "" {
			ps.query = ""
			return nil
		}
		ps.focus = false
		return nil
	case "up":
		if ps.sel > 0 {
			ps.sel--
		} else {
			ps.focus = false
		}
		return nil
	case "down":
		if ps.sel < len(items)-1 {
			ps.sel++
		}
		return nil
	case "tab":
		ps.filter = (ps.filter + 1) % len(kanbanPaneFilters)
		ps.sel = 0
		return nil
	case "shift+tab":
		ps.filter = (ps.filter + len(kanbanPaneFilters) - 1) % len(kanbanPaneFilters)
		ps.sel = 0
		return nil
	case "backspace":
		if r := []rune(ps.query); len(r) > 0 {
			ps.query = string(r[:len(r)-1])
			ps.sel = 0
		}
		return nil
	case "enter":
		if ps.sel < len(items) {
			a.prefillKanbanPrompt(items[ps.sel])
		}
		return nil
	case "ctrl+c":
		ps.focus = false
		return nil
	}
	if m.Type == tea.KeyRunes {
		s := string(m.Runes)
		if s == "p" && ps.query == "" {
			ps.all = !ps.all
			ps.sel = 0
			return nil
		}
		ps.query += s
		ps.sel = 0
	}
	return nil
}

// prefillKanbanPrompt puts an item's structured prompt in the composer for
// the user to edit and send. It is only text in the editor: sending it takes
// the typed-prompt path, admission included.
func (a *App) prefillKanbanPrompt(it kanban.Item) {
	if a.kb != nil {
		a.kb.pane.focus = false
		a.kb.pane.query = ""
	}
	a.editor.SetValue(kanbanPrompt(it, a.kanbanDirReachable(it.Dir)))
	a.editor.CursorEnd()
	a.relayout()
}

// kanbanPrompt is the structured prompt for an item. What the model is told
// to do depends on the list the item is in.
func kanbanPrompt(it kanban.Item, reachable bool) string {
	var b strings.Builder
	id := it.Short()
	switch it.List {
	case kanban.Review:
		fmt.Fprintf(&b, "Review kanban item %s: %s\n\n", id, it.Title)
		fmt.Fprintf(&b, "Session %s noted this as open work in project %s. First confirm it is still outstanding against the current code: search for it and read what it names. ", kanbanShortSession(it.SessionID), it.Project)
		fmt.Fprintf(&b, "If it is already resolved or obsolete, KanbanMove %s to done with a note of the evidence. If it is real, KanbanMove %s to in_progress, do the work, verify it (build and tests), then KanbanMove it to done with a note of what changed; if you cannot finish, move it to blocked with the blocker as the note.", id, id)
	case kanban.Blocked:
		fmt.Fprintf(&b, "Unblock kanban item %s: %s\n\n", id, it.Title)
		if note := it.LastNote(); note != "" {
			fmt.Fprintf(&b, "Last blocker note: %s\n\n", note)
		}
		fmt.Fprintf(&b, "Re-check whether the blocker still holds. If it has cleared, KanbanMove %s to in_progress and carry the work through to done, verified. If it still holds, KanbanUpdate %s with what still blocks it and what is needed to clear it, and leave it blocked.", id, id)
	case kanban.InProgress:
		fmt.Fprintf(&b, "Continue kanban item %s: %s\n\n", id, it.Title)
		fmt.Fprintf(&b, "Pick up where the last session left off (KanbanSearch %s shows its history), finish and verify the work, then KanbanMove it to done with a note of what changed; if you cannot finish, move it to blocked with the blocker.", id)
	default:
		fmt.Fprintf(&b, "Work on kanban item %s: %s\n\n", id, it.Title)
		fmt.Fprintf(&b, "KanbanMove %s to in_progress, investigate and implement it, verify it (build and tests), then KanbanMove it to done with a note of what changed. If you cannot finish, move it to blocked with the blocker as the note.", id)
	}
	if it.Body != "" {
		fmt.Fprintf(&b, "\n\nItem details:\n%s", it.Body)
	}
	if !reachable && it.Dir != "" {
		fmt.Fprintf(&b, "\n\nThe item was filed from %s, which is outside this session's roots: /add-dir it first if the work is there.", it.Dir)
	}
	return sessionsync.CleanPrompt(b.String())
}

func kanbanShortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "(web)"
	}
	return id
}

// kanbanEvent renders the wrap-up's start and summary.
func (a *App) kanbanEvent(m agent.Event) {
	switch m.Phase {
	case agent.KanbanPhaseStart:
		a.addSystem("kanban: filing open work and closing finished items")
	case agent.KanbanPhaseDone:
		s := m.Kanban
		if s == nil || s.Added+s.Moved+s.Updated == 0 {
			a.addSystem("kanban: no changes")
			return
		}
		var parts []string
		if s.Added > 0 {
			parts = append(parts, fmt.Sprintf("%d added to review", s.Added))
		}
		if s.Moved > 0 {
			parts = append(parts, fmt.Sprintf("%d moved to done", s.Moved))
		}
		if s.Updated > 0 {
			parts = append(parts, fmt.Sprintf("%d updated", s.Updated))
		}
		a.addSystem("kanban: " + strings.Join(parts, " · ") + " — /kanban to review")
	}
}

func kanbanStoreOf(a *App) *kanban.Store {
	s, _ := a.kanbanStore()
	return s
}

func kanbanSourceOf(a *App) *kanban.Source {
	_, src := a.kanbanStore()
	return src
}
