package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/tui/components"
)

// dirPickState drives the /add-dir directory picker.
type dirPickState struct {
	path        string
	entries     []os.DirEntry
	selected    int
	loading     bool
	err         error
	confirming  bool
	confirmPath string
}

// workspaceMapReadyMsg carries the result of a background repo-map scan for
// an added workspace directory.
type workspaceMapReadyMsg struct {
	dir string
	m   repomap.Map
	err error
}

// loadWorkspaceDirs restores persisted workspace directories and returns a set
// of tea.Cmds that scan each one for a repo map off the Update loop.
func (a *App) loadWorkspaceDirs() []tea.Cmd {
	dirs, err := projectregistry.ResolveWorkspaceDirs(a.workdir, a.settings.WorkspaceDirs)
	if err != nil {
		a.addSystem("workspace dirs: " + err.Error())
		return nil
	}
	a.workspaceDirs = dirs
	var cmds []tea.Cmd
	for _, d := range dirs {
		cmds = append(cmds, a.workspaceMapScanCmd(d))
	}
	return cmds
}

// workspaceMapScanCmd returns a tea.Cmd that scans dir for a repo map.
func (a *App) workspaceMapScanCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		m := repomap.Scan(context.Background(), dir)
		return workspaceMapReadyMsg{dir: dir, m: m}
	}
}

// enterAddDir opens the directory picker rooted at the user's home directory.
func (a *App) enterAddDir() tea.Cmd {
	home, err := os.UserHomeDir()
	if err != nil {
		home = a.workdir
	}
	a.dirPickState = dirPickState{path: home}
	return a.refreshDirPick()
}

// refreshDirPick reloads the listing for the current picker path.
func (a *App) refreshDirPick() tea.Cmd {
	st := &a.dirPickState
	st.loading = true
	path := st.path
	return func() tea.Msg {
		entries, err := os.ReadDir(path)
		return dirPickLoadedMsg{path: path, entries: entries, err: err}
	}
}

// dirPickLoadedMsg carries the result of reading one directory.
type dirPickLoadedMsg struct {
	path    string
	entries []os.DirEntry
	err     error
}

// handleDirPickLoaded installs a fresh directory listing.
func (a *App) handleDirPickLoaded(m dirPickLoadedMsg) {
	st := &a.dirPickState
	st.loading = false
	if m.err != nil {
		st.err = m.err
		st.entries = nil
		return
	}
	st.err = nil
	st.entries = filterAndSortEntries(m.entries)
	st.selected = 0
}

// filterAndSortEntries puts directories first, then files, each alphabetical.
func filterAndSortEntries(entries []os.DirEntry) []os.DirEntry {
	var dirs, files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	return append(dirs, files...)
}

// selectedDirPickEntry returns the currently highlighted entry, if it exists.
func (st *dirPickState) selectedEntry() (os.DirEntry, bool) {
	if st.selected < 0 || st.selected >= len(st.entries) {
		return nil, false
	}
	return st.entries[st.selected], true
}

// selectedDirPickPath returns the full path of the highlighted entry.
func (st *dirPickState) selectedPath() string {
	if e, ok := st.selectedEntry(); ok {
		return filepath.Join(st.path, e.Name())
	}
	return ""
}

// handleAddDirKey routes keys while the directory picker is open.
func (a *App) handleAddDirKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.dirPickState
	if st.confirming {
		switch strings.ToLower(m.String()) {
		case "y":
			st.confirming = false
			return a, a.addSelectedWorkspaceDir()
		case "n", "esc", "left":
			st.confirming = false
			return a, nil
		}
		return a, nil
	}
	switch m.String() {
	case "up":
		if st.selected > 0 {
			st.selected--
		}
	case "down":
		if st.selected < len(st.entries)-1 {
			st.selected++
		}
	case "right", "enter":
		if e, ok := st.selectedEntry(); ok && e.IsDir() {
			st.path = filepath.Join(st.path, e.Name())
			return a, a.refreshDirPick()
		}
	case "left":
		parent := filepath.Dir(st.path)
		if parent != st.path && parent != "" {
			st.path = parent
			return a, a.refreshDirPick()
		}
	case "a":
		if e, ok := st.selectedEntry(); ok && e.IsDir() {
			st.confirming = true
			st.confirmPath = filepath.Join(st.path, e.Name())
		}
	case "esc":
		a.pop()
	}
	return a, nil
}

// addWorkspaceDirCmd persists an absolute or relative path as a workspace
// directory. It is the direct /add-dir <path> path, bypassing the picker.
func (a *App) addWorkspaceDirCmd(arg string) tea.Cmd {
	dir := arg
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.workdir, dir)
	}
	return func() tea.Msg {
		if err := projectregistry.AddWorkspaceDir(a.workdir, dir); err != nil {
			return workspaceDirAddedMsg{err: err}
		}
		repomap.Scan(context.Background(), dir)
		return workspaceDirAddedMsg{dir: dir}
	}
}

// addSelectedWorkspaceDir persists the selected directory and updates the
// running session's confinement.
func (a *App) addSelectedWorkspaceDir() tea.Cmd {
	st := &a.dirPickState
	dir := st.confirmPath
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		if err := projectregistry.AddWorkspaceDir(a.workdir, dir); err != nil {
			return workspaceDirAddedMsg{err: err}
		}
		repomap.Scan(context.Background(), dir)
		return workspaceDirAddedMsg{dir: dir}
	}
}

// workspaceDirAddedMsg reports the result of adding a workspace directory.
type workspaceDirAddedMsg struct {
	dir string
	err error
}

// handleWorkspaceDirAdded installs an added workspace directory into the live
// session and starts a background repo-map scan.
func (a *App) handleWorkspaceDirAdded(m workspaceDirAddedMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("add-dir: " + m.err.Error())
		return nil
	}
	// Idempotently extend the live list.
	for _, d := range a.workspaceDirs {
		if d == m.dir {
			a.pop()
			a.addSystem("workspace directory already added: " + m.dir)
			return nil
		}
	}
	a.workspaceDirs = append(a.workspaceDirs, m.dir)
	if a.agent != nil && a.agent.Cwd() != nil {
		_ = a.agent.Cwd().AddRoot(m.dir)
	}
	a.invalidateAgentSession()
	a.addSystem("added workspace directory: " + m.dir)
	a.pop()
	return a.workspaceMapScanCmd(m.dir)
}

// renderAddDirView draws the directory picker and optional confirmation pane.
func (a *App) renderAddDirView() string {
	st := &a.dirPickState
	width := a.contentWidth()

	var lines []string
	lines = append(lines, components.MutedStyle.Render("navigation: ↑↓  enter: open  a: add selected  esc: cancel"))
	lines = append(lines, components.MutedStyle.Render("path: "+st.path))
	if st.err != nil {
		lines = append(lines, "error: "+st.err.Error())
	} else if st.loading {
		lines = append(lines, components.MutedStyle.Render("loading..."))
	} else {
		for i, e := range st.entries {
			selected := i == st.selected
			style := components.MutedStyle
			if selected {
				style = components.EmphStyle
			}
			icon := "📄 "
			if e.IsDir() {
				icon = "📁 "
			}
			lines = append(lines, components.Cursor(selected)+style.Render(icon+e.Name()))
		}
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}

	var panels []string
	panels = append(panels, components.Panel{
		Title:  "add workspace directory",
		Body:   body,
		Width:  width,
		Accent: lipgloss.TerminalColor(components.ColorTeal),
	}.View())

	if st.confirming {
		panels = append(panels, "")
		panels = append(panels, components.Panel{
			Title: "confirm",
			Body: components.MutedStyle.Render("Add ") +
				components.EmphStyle.Render(st.confirmPath) +
				components.MutedStyle.Render(" as an additional workspace root?\n") +
				components.MutedStyle.Render("y: yes  n: no"),
			Width:  width,
			Accent: lipgloss.TerminalColor(components.ColorAmber),
		}.View())
	}

	return strings.Join(panels, "\n")
}
