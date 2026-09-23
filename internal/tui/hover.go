package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/clipboard"
	"github.com/vulnetix/signet/internal/tui/components"
)

// hoverTarget records what the pointer is over. It is derived every frame
// from the last mouse position and the provenance the renderers attach to
// each transcript line, so it never goes stale the way a one-shot hit-test
// would: expanding with ctrl+o or streaming a delta re-derives the target
// without another mouse event.
type hoverTarget struct {
	file      bool // a Read result carrying a path — save and copy offered
	text      bool // any panel with text — save and copy offered
	collapsed bool // a truncated panel — ctrl+o offered
	session   bool // the footer's session segment — ctrl+x offered
	msg       int  // message index for file/collapsed/text targets
}

// recomputeHover re-derives a.hover from the last mouse position, the current
// rendered frame and the footer geometry. It is called from chatView after
// lastFrame is built, so the hint always matches the frame on screen.
func (a *App) recomputeHover() {
	a.hover = hoverTarget{}
	if !a.mousePresent || a.view != viewChat {
		return
	}
	x, y := a.mouseX, a.mouseY
	if a.hitSession(x, y) {
		a.hover.session = true
		return
	}
	p, ok := contentPos(x, y, a.lastFrame)
	if !ok || p.Line < 0 || p.Line >= len(a.lastFrame.lines) {
		return
	}
	line := a.lastFrame.lines[p.Line]
	if line.Owner < 0 {
		return
	}
	a.hover.msg = line.Owner
	a.hover.file = line.File
	a.hover.text = line.Copyable
	a.hover.collapsed = line.Collapsed
}

// hitSession reports whether a screen cell lies over the footer's session
// segment. The segment lives on the footer's second content line (rule, line
// 1, line 2), right-aligned, at the column span SessionSpan returns.
func (a *App) hitSession(x, y int) bool {
	h := a.footerHeight()
	top := a.height - 1 - h
	if y < top || y >= top+h {
		return false
	}
	if y-top != 2 {
		return false
	}
	col, width, ok := a.footer.SessionSpan()
	if !ok {
		return false
	}
	left := a.contentLeft()
	return x >= left+col && x < left+col+width
}

// hoverHint renders the footer hint for the current hover target: the actions
// available and the keybinding for each. It is empty when nothing actionable
// is under the pointer.
func (a *App) hoverHint() string {
	var pairs []string
	if a.hover.text {
		pairs = append(pairs, "ctrl+s", "save "+a.hoverSaveName())
		pairs = append(pairs, "ctrl+c", "copy")
	}
	if a.hover.collapsed {
		pairs = append(pairs, "ctrl+o", "expand all")
	}
	if a.hover.session {
		pairs = append(pairs, "ctrl+x", "copy session id")
	}
	if len(pairs) == 0 {
		return ""
	}
	return components.HelpBar(pairs...)
}

// hoverSaveName returns the suggested file name for saving the hovered panel:
// the basename for a Read file panel, otherwise a generated
// signet-<session>-<idx>.<ext> name. The name is unqualified (no directory) —
// finishSaveFile joins a relative path against the workdir.
func (a *App) hoverSaveName() string {
	if path := a.filePanelPath(a.hover.msg); path != "" {
		name := filepath.Base(path)
		return truncateSaveName(name)
	}
	ext := ".md"
	if a.hover.msg >= 0 && a.hover.msg < len(a.messages) {
		if a.messages[a.hover.msg].Role == "tool" {
			ext = ".txt"
		}
	}
	base := fmt.Sprintf("signet-%s-%d%s", a.sessionDisplay(), a.hover.msg, ext)
	if a.sessionDisplay() == "" {
		base = fmt.Sprintf("signet-%d%s", a.hover.msg, ext)
	}
	return truncateSaveName(base)
}

// truncateSaveName truncates a save name to 40 runes, mirroring the old
// hoverFileName cap.
func truncateSaveName(name string) string {
	const maxLen = 40
	runes := []rune(name)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return name
}

// filePanelPath returns the hovered message's file path, or "" when the index
// is out of range or the message is not a file panel.
func (a *App) filePanelPath(idx int) string {
	if idx < 0 || idx >= len(a.messages) {
		return ""
	}
	return a.messages[idx].FilePath()
}

// copyHoveredPanel puts the hovered panel's content on the clipboard. It
// shares the copiedMsg path with copyPrompt and copySelection.
func (a *App) copyHoveredPanel(idx int) tea.Cmd {
	if idx < 0 || idx >= len(a.messages) {
		return nil
	}
	text := a.messages[idx].FileText()
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		method, err := clipboard.Copy(text)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		note := ""
		// OSC 52 is a silent-drop risk for large payloads (xterm's
		// maxStringParseSize, tmux without set-clipboard on); say so.
		if method == "osc52" && len(text) > 8*1024 {
			note = " — large payload, terminal may have dropped it"
		}
		return copiedMsg{text: fmt.Sprintf("copied file to clipboard (%s)%s", method, note)}
	}
}

// copySessionID puts the full session id on the clipboard.
func (a *App) copySessionID() tea.Cmd {
	if a.sessionID == "" {
		return nil
	}
	id := a.sessionID
	return func() tea.Msg {
		method, err := clipboard.Copy(id)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		return copiedMsg{text: "copied session id to clipboard (" + method + ")"}
	}
}

// startSaveFile opens the save-file flow for a hovered panel: the composer
// becomes a destination-path prompt pre-filled with the suggested name, and
// enter writes the panel's content there. It is inert for a panel with no
// text.
func (a *App) startSaveFile(idx int) tea.Cmd {
	if idx < 0 || idx >= len(a.messages) {
		return nil
	}
	if a.messages[idx].Text() == "" {
		return nil
	}
	a.saveFileMsg = idx
	a.saveFileMode = true
	a.editor.Reset()
	a.clearAutocomplete()
	a.editor.SetValue(a.hoverSaveName())
	a.editor.CursorEnd()
	return nil
}

// handleSaveFileKey routes keys while the save-file path prompt is open.
func (a *App) handleSaveFileKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "enter":
		path := strings.TrimSpace(a.editor.Value())
		if path == "" {
			a.addSystem("save cancelled: path required")
			a.cancelSaveFile()
			return nil
		}
		return a.finishSaveFile(path)
	case "esc":
		a.cancelSaveFile()
		a.addSystem("save cancelled")
		return nil
	}
	return a.editor.Update(m)
}

// finishSaveFile writes the hovered file's content to path. Relative paths
// resolve against the working directory; absolute paths are used as-is. The
// user is the actor here, not the model, so the path is deliberately not
// confined to the working directory.
func (a *App) finishSaveFile(path string) tea.Cmd {
	var content string
	if a.saveFileMsg >= 0 && a.saveFileMsg < len(a.messages) {
		content = a.messages[a.saveFileMsg].FileText()
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(a.workdir, full)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		a.addSystem("save failed: " + err.Error())
		a.cancelSaveFile()
		return nil
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		a.addSystem("save failed: " + err.Error())
	} else {
		a.addSystem(fmt.Sprintf("saved %s (%d bytes)", path, len(content)))
	}
	a.cancelSaveFile()
	return nil
}

// cancelSaveFile leaves the save-file flow without writing anything.
func (a *App) cancelSaveFile() {
	a.saveFileMode = false
	a.saveFileMsg = -1
	a.editor.Reset()
	a.clearLoadedPrompt()
}
