package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/tui/components"
)

// noFileSelection is the fileIndex value meaning "no candidate is highlighted".
const noFileSelection = -1

// fileListTTL bounds how stale the workspace listing may be before it is
// re-enumerated. A file can be created or deleted between keystrokes, so the
// answer is cached, not frozen.
const fileListTTL = 30 * time.Second

// imageExtensions are filtered out of the file chooser this round. See
// docs/image-attachments.md for the deferred image attachment design.
var imageExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"bmp": true, "ico": true, "tif": true, "tiff": true, "avif": true,
}

// filesLoadedMsg carries the result of an async workspace listing.
type filesLoadedMsg struct {
	files []string
	err   error
}

// fileListCmd enumerates the workspace off the Update loop. It captures the
// workdir into the closure before launching, so it never races the App.
func (a *App) fileListCmd() tea.Cmd {
	workdir := a.workdir
	// Only directories the tools accept as roots are listed, so a persisted
	// workspace dir the Cwd refuses never surfaces in the chooser.
	workspaceDirs := a.rootSet().Roots()[1:]
	a.filesLoading = true
	traceCtx := a.toolContext(context.Background(), "Glob", "")
	return func() tea.Msg {
		ctx := traceCtx
		var files []string
		seen := map[string]bool{}
		roots := append([]string{workdir}, workspaceDirs...)
		for i, root := range roots {
			g := &tools.Glob{
				Cwd:        tools.NewCwd(root),
				Root:       root,
				MaxResults: 5000,
			}
			res, err := g.Execute(ctx, map[string]any{"pattern": "**/*"})
			if err != nil {
				return filesLoadedMsg{err: err}
			}
			for _, p := range strings.Split(res.Content, "\n") {
				if p == "" {
					continue
				}
				if imageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))] {
					continue
				}
				// The primary root stays relative; extra roots are returned as
				// absolute paths so they can be handed straight back to Read.
				if i > 0 && !filepath.IsAbs(p) {
					p = filepath.Join(root, p)
				}
				if seen[p] {
					continue
				}
				seen[p] = true
				files = append(files, p)
			}
		}
		return filesLoadedMsg{files: files}
	}
}

// fileListIfStale returns a listing command when the cache is stale or empty
// and none is already running, else nil.
func (a *App) fileListIfStale() tea.Cmd {
	if a.filesLoading {
		return nil
	}
	if !a.filesLoadedAt.IsZero() && time.Since(a.filesLoadedAt) < fileListTTL {
		return nil
	}
	return a.fileListCmd()
}

// handleFilesLoaded installs a completed workspace listing.
func (a *App) handleFilesLoaded(m filesLoadedMsg) tea.Cmd {
	a.filesLoading = false
	if m.err == nil {
		a.files = m.files
		a.filesLoadedAt = time.Now()
	}
	return nil
}

// filePrefix returns the @-prefixed token the cursor is inside, if any. It
// scans backwards from the cursor to the nearest '@' and requires that no
// whitespace sits between the '@' and the cursor.
func (a *App) filePrefix() (at int, prefix string, ok bool) {
	value := a.editor.Value()
	runes := []rune(value)
	off := a.editor.CursorOffset()
	if off > len(runes) {
		off = len(runes)
	}
	for i := off - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			return 0, "", false
		}
		if runes[i] == '@' {
			for j := i + 1; j < off; j++ {
				if unicode.IsSpace(runes[j]) {
					return 0, "", false
				}
			}
			return i, string(runes[i+1 : off]), true
		}
	}
	return 0, "", false
}

// fsListCap bounds one path-mode directory listing so a huge directory cannot
// stall the chooser.
const fsListCap = 2000

// fsListing is one cached, single-level directory listing for the path-mode
// chooser. Entry names carry a trailing "/" for directories.
type fsListing struct {
	entries  []string
	loadedAt time.Time
}

// fsListedMsg carries the result of an async path-mode directory listing.
type fsListedMsg struct {
	dir     string
	entries []string
}

// isPathToken reports whether an @-prefix names a filesystem path rather than
// a filter over the workspace listing. Path tokens browse one directory at a
// time and may walk above the session roots; what they name is only admitted
// once its directory is a root (see resolveAttachment).
func isPathToken(prefix string) bool {
	switch {
	case prefix == "..", prefix == "~":
		return true
	case strings.HasPrefix(prefix, "/"), strings.HasPrefix(prefix, "~/"),
		strings.HasPrefix(prefix, "../"), strings.HasPrefix(prefix, "./"):
		return true
	}
	return false
}

// splitPathToken splits a path token into the directory part as typed (with
// its trailing slash), the name filter after it, and the absolute directory
// to list. A leading "/" that names no real directory falls back to the
// workdir, matching how @/path resolves root-relative.
func (a *App) splitPathToken(prefix string) (dirPart, base, dir string) {
	if prefix == ".." || prefix == "~" {
		prefix += "/"
	}
	i := strings.LastIndex(prefix, "/")
	dirPart, base = prefix[:i+1], prefix[i+1:]
	dir = expandHomePath(dirPart)
	if filepath.IsAbs(dir) && !strings.HasPrefix(dirPart, "~") {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			dir = filepath.Join(a.workdir, dir)
		}
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.workdir, dir)
	}
	return dirPart, base, filepath.Clean(dir)
}

// pathListIfStale returns a listing command for the directory the current
// path token is in, when that listing is missing or stale and not already
// loading. It returns nil for a non-path token.
func (a *App) pathListIfStale() tea.Cmd {
	_, prefix, ok := a.filePrefix()
	if !ok || !isPathToken(prefix) {
		return nil
	}
	_, _, dir := a.splitPathToken(prefix)
	if a.fsLoading[dir] {
		return nil
	}
	if l, ok := a.fsLists[dir]; ok && time.Since(l.loadedAt) < fileListTTL {
		return nil
	}
	if a.fsLoading == nil {
		a.fsLoading = map[string]bool{}
	}
	a.fsLoading[dir] = true
	return fsListCmd(dir)
}

// fsListCmd reads one directory off the Update loop. Only entry names are
// read — never file contents — and a symlink is stat'ed to mark it as a
// directory but not followed any further.
func fsListCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return fsListedMsg{dir: dir}
		}
		var out []string
		for _, e := range ents {
			if len(out) >= fsListCap {
				break
			}
			name := e.Name()
			isDir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 {
				if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
					isDir = info.IsDir()
				}
			}
			if isDir {
				out = append(out, name+"/")
				continue
			}
			if imageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))] {
				continue
			}
			out = append(out, name)
		}
		return fsListedMsg{dir: dir, entries: out}
	}
}

// handleFSListed installs a completed path-mode listing. A failed read is
// cached empty so an unreadable directory is not re-read on every keystroke.
func (a *App) handleFSListed(m fsListedMsg) {
	delete(a.fsLoading, m.dir)
	if a.fsLists == nil {
		a.fsLists = map[string]fsListing{}
	}
	a.fsLists[m.dir] = fsListing{entries: m.entries, loadedAt: time.Now()}
	a.relayout()
}

// pathCandidates lists the entries of the path token's directory whose name
// starts with the typed filter, spelled the way the user typed the directory.
// Hidden entries appear only once the filter itself starts with a dot.
func (a *App) pathCandidates(prefix string) []string {
	dirPart, base, dir := a.splitPathToken(prefix)
	lower := strings.ToLower(base)
	var out []string
	for _, name := range a.fsLists[dir].entries {
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), lower) {
			continue
		}
		out = append(out, dirPart+name)
	}
	return out
}

// candidateOutsideRoots reports whether a path-mode candidate lies outside
// every session root. roots is computed once per render by the caller.
func (a *App) candidateOutsideRoots(roots *tools.Cwd, cand string) bool {
	if !isPathToken(cand) {
		return false
	}
	_, _, dir := a.splitPathToken(cand)
	return !roots.Contains(dir)
}

// fileCandidates returns the paths that match the current @-prefix.
func (a *App) fileCandidates() []string {
	_, prefix, ok := a.filePrefix()
	if !ok {
		return nil
	}
	if isPathToken(prefix) {
		return a.pathCandidates(prefix)
	}
	cands := filterCandidates(a.files, prefix)
	var out []string
	for _, p := range cands {
		if imageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// filePickerVisible reports whether the chooser has anything to draw. The slash
// popup wins when both could show; the agent picker is hidden while an @-prefix
// is active because @ is now reserved for file references.
func (a *App) filePickerVisible() bool {
	if a.view != viewChat || len(a.autocomplete) > 0 || a.dirPickState.open {
		return false
	}
	_, prefix, ok := a.filePrefix()
	if !ok {
		return false
	}
	if a.fileDismissed != "" && prefix == a.fileDismissed {
		return false
	}
	cands := a.fileCandidates()
	return len(cands) > 0
}

// filePickHeight returns the rendered height of the picker when visible.
func (a *App) filePickHeight() int {
	if !a.filePickerVisible() {
		return 0
	}
	return lipgloss.Height(a.renderFilePicker())
}

// cycleFile moves the highlight up or down through the candidate list.
func (a *App) cycleFile(delta int) tea.Cmd {
	cyclePicker(a.fileCandidates(), &a.fileIndex, delta)
	return nil
}

// selectedFile returns the currently highlighted candidate, if any.
func (a *App) selectedFile() (string, bool) {
	return selectedCandidate(a.fileCandidates(), a.fileIndex)
}

// acceptFilePick replaces the @-prefix with the chosen path and triggers an
// attachment validation immediately.
func (a *App) acceptFilePick() tea.Cmd {
	at, _, ok := a.filePrefix()
	if !ok {
		return nil
	}
	path, ok := a.selectedFile()
	if !ok {
		return nil
	}
	cursor := a.editor.CursorOffset()

	var replacement string
	if strings.ContainsAny(path, " \t") {
		replacement = `@"` + path + `" `
	} else {
		replacement = `@` + path + ` `
	}

	a.editor.ReplaceRange(at, cursor, replacement)
	a.clearFilePickerOnAccept()
	a.relayout()
	return a.syncAttachments()
}

// descendFilePick moves a path token into the highlighted directory, leaving
// the chooser open on its entries. It reports false when the highlight is not
// a path-mode directory, so the caller accepts instead.
func (a *App) descendFilePick() (tea.Cmd, bool) {
	at, prefix, ok := a.filePrefix()
	if !ok || !isPathToken(prefix) {
		return nil, false
	}
	path, ok := a.selectedFile()
	if !ok || !strings.HasSuffix(path, "/") || strings.ContainsAny(path, " \t") {
		return nil, false
	}
	a.editor.ReplaceRange(at, a.editor.CursorOffset(), "@"+path)
	a.clearFilePickerOnAccept()
	a.relayout()
	return a.pathListIfStale(), true
}

// clearFilePickerOnAccept resets the chooser highlight but leaves the listing
// cached for the next open.
func (a *App) clearFilePickerOnAccept() {
	a.fileIndex = noFileSelection
	a.fileScroll = 0
	a.fileDismissed = ""
}

// dismissFilePicker closes the chooser on esc/left and remembers the token so
// it does not immediately reopen.
func (a *App) dismissFilePicker() {
	_, prefix, ok := a.filePrefix()
	if ok {
		a.fileDismissed = prefix
	}
	a.fileIndex = noFileSelection
	a.fileScroll = 0
}

// handleFilePickKey routes keys while the file chooser is open. It claims the
// navigation and accept keys; all other keys fall through to the editor so
// typing continues to filter the list.
func (a *App) handleFilePickKey(m tea.KeyMsg) (tea.Cmd, bool) {
	switch m.String() {
	case "up":
		a.cycleFile(-1)
		return nil, true
	case "down":
		a.cycleFile(1)
		return nil, true
	case "right", "tab":
		if cmd, ok := a.descendFilePick(); ok {
			return cmd, true
		}
		return a.acceptFilePick(), true
	case "enter":
		return a.acceptFilePick(), true
	case "esc", "left":
		a.dismissFilePicker()
		return nil, true
	}
	return nil, false
}

// fileSearchLine renders the active filter above the candidate list.
func (a *App) fileSearchLine() string {
	_, prefix, _ := a.filePrefix()
	return components.MutedStyle.Render("filter  ") +
		components.EmphStyle.Render(prefix+"▌")
}

// renderFilePicker draws the scrolling, filtered file list.
func (a *App) renderFilePicker() string {
	cands := a.fileCandidates()
	meta := pickerCounter(a.fileIndex, len(cands))
	var mark func(string) string
	if _, prefix, _ := a.filePrefix(); isPathToken(prefix) {
		// Rows outside every session root carry an amber triangle: picking
		// one asks the user to adopt its directory as a root first.
		roots := a.rootSet()
		mark = func(cand string) string {
			if a.candidateOutsideRoots(roots, cand) {
				return components.WarnStyle.Render("⚠ ")
			}
			return "  "
		}
	}
	rendered, newScroll := renderPickerMarked("files", meta, a.contentWidth(), cands, a.fileIndex, a.fileScroll, []string{a.fileSearchLine()}, lipgloss.TerminalColor(components.ColorTeal), mark)
	a.fileScroll = newScroll
	return rendered
}
