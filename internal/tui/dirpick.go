package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/tui/components"
)

// noDirSelection is the selected value meaning "no directory is highlighted",
// mirroring noFileSelection in the @ file chooser.
const noDirSelection = -1

// dirPickState drives the inline /add-dir directory chooser. It lives in the
// chat view, exactly like the @ file chooser: the composer text is the filter,
// up/down move the highlight, enter confirms the highlighted directory, and
// esc cancels.
type dirPickState struct {
	open        bool
	dirs        []string // candidate directory paths, absolute
	filter      string   // mirrors the composer text while the picker is open
	selected    int
	scroll      int
	loading     bool
	err         error
	confirming  bool
	confirmPath string
	gen         uint64 // incremented on each open/close; stale loads are ignored
}

const (
	// dirPickMaxResults keeps the picker responsive on large trees.
	dirPickMaxResults = 2000
	// dirPickMaxDepth limits how far below the root we enumerate.
	dirPickMaxDepth = 5
	// dirPickBudget caps the time we spend scanning directories.
	dirPickBudget = 3 * time.Second
)

// workspaceMapReadyMsg carries the result of a background repo-map scan for
// an added workspace directory.
type workspaceMapReadyMsg struct {
	dir string
	m   repomap.Map
	err error
}

// dirPickLoadedMsg carries the result of scanning directories for the picker.
type dirPickLoadedMsg struct {
	dirs []string
	err  error
	gen  uint64
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

// workspaceDirsIgnoredNoteStillValid reports whether the "project
// workspace_dirs ignored" resolve note still applies. Once every proposed
// directory is already an active root (accepted through the trust gate or
// /add-dir), the note is misleading and is suppressed.
func (a *App) workspaceDirsIgnoredNoteStillValid() bool {
	proj, err := config.LoadProject(a.workdir)
	if err != nil || len(proj.WorkspaceDirs) == 0 {
		return true
	}
	active := map[string]bool{}
	for _, d := range a.workspaceDirs {
		active[d] = true
	}
	for _, d := range proj.WorkspaceDirs {
		if !active[workspaceDirAbs(a.workdir, d)] {
			return true
		}
	}
	return false
}

// workspaceDirAbs absolutises a project-proposed directory against workdir,
// resolving symlinks when the directory exists.
func workspaceDirAbs(workdir, dir string) string {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workdir, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return eval
	}
	return abs
}

// workspaceMapScanCmd returns a tea.Cmd that scans dir for a repo map.
func (a *App) workspaceMapScanCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		m := repomap.Scan(context.Background(), dir)
		return workspaceMapReadyMsg{dir: dir, m: m}
	}
}

// openAddDirPicker opens the inline directory chooser rooted at the user's
// home directory. The composer becomes the filter input, so the @ chooser's
// type-to-filter, up/down, enter, esc behaviour is reused unchanged.
func (a *App) openAddDirPicker() tea.Cmd {
	home, err := os.UserHomeDir()
	if err != nil {
		home = a.workdir
	}
	st := &a.dirPickState
	st.open = true
	st.dirs = nil
	st.filter = ""
	st.selected = noDirSelection
	st.scroll = 0
	st.loading = true
	st.err = nil
	st.confirming = false
	st.confirmPath = ""
	st.gen++
	a.editor.Reset()
	a.editor.Masked = false
	a.clearLoadedPrompt()
	a.clearAutocomplete()
	a.relayout()
	return a.loadDirPickCmd(home)
}

// closeAddDirPicker closes the inline chooser and returns the composer to an
// empty, normal prompt.
func (a *App) closeAddDirPicker() {
	st := &a.dirPickState
	st.open = false
	st.filter = ""
	st.selected = noDirSelection
	st.scroll = 0
	st.loading = false
	st.err = nil
	st.confirming = false
	st.confirmPath = ""
	st.gen++
	a.editor.Reset()
	a.editor.Masked = false
	a.clearAutocomplete()
	a.relayout()
}

// loadDirPickCmd starts a background scan of directories under root.
func (a *App) loadDirPickCmd(root string) tea.Cmd {
	gen := a.dirPickState.gen
	return func() tea.Msg {
		dirs, err := listDirectories(root)
		return dirPickLoadedMsg{dirs: dirs, err: err, gen: gen}
	}
}

// listDirectories walks root and returns a sorted list of absolute directory
// paths. It skips system directories, hidden directories, dependency/output
// directories, and symlinked directories, and it is bounded by depth, result
// count, and a time budget. Once a bound is hit the walk stops entirely, so
// the picker can never hang enumerating a large home tree.
func listDirectories(root string) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", root)
	}

	deadline := time.Now().Add(dirPickBudget)
	skip := map[string]bool{}
	for _, d := range projectregistry.DefaultSkipDirs {
		skip[d] = true
	}

	hardSkip := map[string]bool{}
	if runtime.GOOS != "windows" {
		for _, p := range []string{"/proc", "/sys", "/dev", "/run"} {
			hardSkip[p] = true
		}
		home, _ := os.UserHomeDir()
		if home != "" {
			hardSkip[filepath.Join(home, "Library")] = true
			hardSkip[filepath.Join(home, ".Trash")] = true
			hardSkip[filepath.Join(home, "snap")] = true
		}
	}

	var dirs []string
	_ = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(dirs) >= dirPickMaxResults || time.Now().After(deadline) {
			return fs.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		if hardSkip[path] {
			return fs.SkipDir
		}

		rel, _ := filepath.Rel(absRoot, path)
		depth := 0
		if rel != "." {
			depth = len(strings.Split(rel, string(filepath.Separator)))
		}
		if depth > dirPickMaxDepth {
			return fs.SkipDir
		}

		name := d.Name()
		if rel != "." && strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		if skip[name] {
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fs.SkipDir
		}

		dirs = append(dirs, path)
		return nil
	})
	sort.Strings(dirs)
	return dirs, nil
}

// handleDirPickLoaded installs a scanned directory listing.
func (a *App) handleDirPickLoaded(m dirPickLoadedMsg) {
	st := &a.dirPickState
	if m.gen != st.gen {
		return
	}
	st.loading = false
	if m.err != nil {
		st.err = m.err
		st.dirs = nil
		a.relayout()
		return
	}
	st.err = nil
	st.dirs = m.dirs
	st.selected = noDirSelection
	st.scroll = 0
	a.relayout()
}

// filtered returns the directories that match the current filter.
func (st *dirPickState) filtered() []string {
	return filterCandidates(st.dirs, st.filter)
}

// setFilter replaces the filter and clamps the selection.
func (st *dirPickState) setFilter(filter string) {
	st.filter = filter
	st.clampSelected()
}

// clampSelected keeps the selection inside the filtered list.
func (st *dirPickState) clampSelected() {
	n := len(st.filtered())
	if n == 0 {
		st.selected = noDirSelection
		return
	}
	if st.selected < 0 {
		st.selected = 0
	} else if st.selected >= n {
		st.selected = n - 1
	}
}

// selectedDir returns the currently highlighted directory, if any.
func (st *dirPickState) selectedDir() (string, bool) {
	return selectedCandidate(st.filtered(), st.selected)
}

// cycle moves the selection up or down through the filtered directories.
func (st *dirPickState) cycle(delta int) {
	cyclePicker(st.filtered(), &st.selected, delta)
}

// addWorkspaceDirCmd adopts an absolute or relative path as a workspace root.
// persist saves it for the project, as /add-dir always does; without it the
// root lasts for this session only (an @path the user confirmed with "s").
// The directory is checked against the live root set first, so a root the
// tools would refuse — an ancestor of the workdir, or an overlap — is never
// saved. The repo-map scan is started by handleWorkspaceDirAdded, so the
// command itself stays fast and the chooser can close immediately.
func (a *App) addWorkspaceDirCmd(arg string, persist bool) tea.Cmd {
	dir := expandHomePath(arg)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.workdir, dir)
	}
	roots := a.rootSet()
	workdir := a.workdir
	return func() tea.Msg {
		abs, err := roots.CheckRoot(dir)
		if err != nil {
			return workspaceDirAddedMsg{dir: dir, err: err, sessionOnly: !persist}
		}
		if persist {
			if err := projectregistry.AddWorkspaceDir(workdir, abs); err != nil {
				return workspaceDirAddedMsg{dir: abs, err: err}
			}
		}
		return workspaceDirAddedMsg{dir: abs, sessionOnly: !persist}
	}
}

// addSelectedWorkspaceDir persists the currently highlighted directory.
func (a *App) addSelectedWorkspaceDir() tea.Cmd {
	return a.addWorkspaceDirCmd(a.dirPickState.confirmPath, true)
}

// workspaceDirAddedMsg reports the result of adding a workspace directory.
type workspaceDirAddedMsg struct {
	dir         string
	err         error
	sessionOnly bool // adopted for this session, not saved for the project
}

// handleWorkspaceDirAdded installs an added workspace directory into the live
// session, starts a background repo-map scan, and settles any @ attachment
// that was waiting on the directory.
func (a *App) handleWorkspaceDirAdded(m workspaceDirAddedMsg) tea.Cmd {
	if a.dirPickState.open {
		a.closeAddDirPicker()
	}
	if m.err != nil {
		a.addSystem("add-dir: " + m.err.Error())
		return a.settleRootConfirm(m.dir, m.err)
	}
	// Idempotently extend the live list.
	for _, d := range a.workspaceDirs {
		if d == m.dir {
			a.addSystem("workspace directory already added: " + m.dir)
			return a.settleRootConfirm(m.dir, nil)
		}
	}
	if a.agent != nil && a.agent.Cwd() != nil {
		if err := a.agent.Cwd().AddRoot(m.dir); err != nil {
			a.addSystem("add-dir: " + err.Error())
			return a.settleRootConfirm(m.dir, err)
		}
	}
	a.workspaceDirs = append(a.workspaceDirs, m.dir)
	a.invalidateAgentSession()
	// The @ chooser's listing now has another root to cover.
	a.filesLoadedAt = time.Time{}
	scope := "for this project"
	if m.sessionOnly {
		scope = "for this session"
	}
	a.addSystem("added workspace directory " + scope + ": " + m.dir)
	return tea.Batch(a.workspaceMapScanCmd(m.dir), a.settleRootConfirm(m.dir, nil))
}

// handleAddDirKey routes keys while the inline directory chooser is open. It
// claims navigation, accept, confirm and cancel; every other key falls through
// to the composer so typing continues to filter the list, exactly like the @
// file chooser.
func (a *App) handleAddDirKey(m tea.KeyMsg) (tea.Cmd, bool) {
	st := &a.dirPickState
	if st.confirming {
		switch strings.ToLower(m.String()) {
		case "y":
			st.confirming = false
			return a.addSelectedWorkspaceDir(), true
		case "n", "esc":
			st.confirming = false
			return nil, true
		}
		return nil, true
	}

	switch m.String() {
	case "up":
		st.cycle(-1)
		return nil, true
	case "down":
		st.cycle(1)
		return nil, true
	case "enter":
		if dir, ok := st.selectedDir(); ok {
			st.confirming = true
			st.confirmPath = dir
		}
		return nil, true
	case "esc":
		a.closeAddDirPicker()
		return nil, true
	}
	return nil, false
}

// dirPickVisible reports whether the inline chooser has anything to draw.
func (a *App) dirPickVisible() bool {
	return a.view == viewChat && a.dirPickState.open
}

// addDirPickHeight returns the rendered height of the chooser when visible.
func (a *App) addDirPickHeight() int {
	if !a.dirPickVisible() {
		return 0
	}
	return lipgloss.Height(a.renderAddDirPicker())
}

// renderAddDirPicker draws the inline directory chooser and its optional
// confirmation pane, reusing renderPicker from the @ file chooser.
func (a *App) renderAddDirPicker() string {
	st := &a.dirPickState
	width := a.contentWidth()
	title := "add workspace directory"

	var panels []string

	switch {
	case st.loading:
		panels = append(panels, components.Panel{
			Title:  title,
			Body:   components.MutedStyle.Render("loading..."),
			Width:  width,
			Accent: lipgloss.TerminalColor(components.ColorTeal),
		}.View())
	case st.err != nil:
		panels = append(panels, components.Panel{
			Title:  title,
			Body:   "error: " + st.err.Error(),
			Width:  width,
			Accent: lipgloss.TerminalColor(components.ColorTeal),
		}.View())
	default:
		cands := st.filtered()
		meta := pickerCounter(st.selected, len(cands))
		help := components.MutedStyle.Render("type: filter  ↑↓: move  enter: add  esc: cancel")
		filterLine := components.MutedStyle.Render("filter  ") +
			components.EmphStyle.Render(st.filter+"▌")
		picker, newScroll := renderPicker(
			title,
			meta,
			width,
			cands,
			st.selected,
			st.scroll,
			[]string{help, filterLine},
			lipgloss.TerminalColor(components.ColorTeal),
		)
		st.scroll = newScroll
		if picker != "" {
			panels = append(panels, picker)
		} else {
			panels = append(panels, components.Panel{
				Title:  title,
				Body:   components.MutedStyle.Render("no directories match"),
				Width:  width,
				Accent: lipgloss.TerminalColor(components.ColorTeal),
			}.View())
		}
	}

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
