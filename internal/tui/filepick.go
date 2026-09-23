package tui

import (
	"context"
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
	workspaceDirs := append([]string{}, a.workspaceDirs...)
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

// fileCandidates returns the paths that match the current @-prefix.
func (a *App) fileCandidates() []string {
	_, prefix, ok := a.filePrefix()
	if !ok {
		return nil
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
	case "right", "tab", "enter":
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
	rendered, newScroll := renderPicker("files", meta, a.contentWidth(), cands, a.fileIndex, a.fileScroll, []string{a.fileSearchLine()}, lipgloss.TerminalColor(components.ColorTeal))
	a.fileScroll = newScroll
	return rendered
}
