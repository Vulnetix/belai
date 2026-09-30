package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/clipboard"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// clipboardImageMsg carries the result of reading an image off the clipboard.
// key is the keypress that started the read, so a clipboard with no image can
// fall back to the text paste it would otherwise have been. It is the zero key
// for /paste-image, which never falls back.
type clipboardImageMsg struct {
	key  tea.KeyMsg
	data []byte
	err  error
	// text is true when a keypress started the read and should paste text when
	// there is no image.
	text bool
}

// readClipboardImageCmd reads the clipboard image off the Update goroutine.
func readClipboardImageCmd(key tea.KeyMsg, text bool) tea.Cmd {
	return func() tea.Msg {
		data, err := clipboard.ReadImage(context.Background())
		return clipboardImageMsg{key: key, data: data, err: err, text: text}
	}
}

// pasteKey handles ctrl+v in the composer. The clipboard is asked for an image
// first; with none, the keypress reaches the editor as the text paste it always
// was, so nothing changes for a user who pastes text.
func (a *App) pasteKey(m tea.KeyMsg) tea.Cmd {
	if !a.settings.ClipboardImagesEnabled() {
		return a.forwardToEditor(m)
	}
	return readClipboardImageCmd(m, true)
}

// handleClipboardImage attaches the image the clipboard held, or falls back.
func (a *App) handleClipboardImage(m clipboardImageMsg) tea.Cmd {
	if m.err != nil || len(m.data) == 0 {
		if m.text {
			// No image: paste text exactly as ctrl+v did before.
			return a.forwardToEditor(m.key)
		}
		msg := clipboard.ErrNoImage.Error()
		if m.err != nil {
			msg = m.err.Error()
		}
		a.addSystem("paste-image: " + msg)
		return nil
	}
	if a.safeImageCount() >= maxAttachedImages {
		a.addSystem(fmt.Sprintf("at most %d images per prompt", maxAttachedImages))
		return nil
	}
	a.pasteSeq++
	name := fmt.Sprintf("clipboard-%d.png", a.pasteSeq)
	img, meta, err := admitImageBytes(name, m.data)
	if err != nil {
		a.addSystem("clipboard image refused: " + err.Error())
		return nil
	}
	// A pasted image has no file: the card names it and says where it came from.
	meta.Trust = "clipboard"
	meta.MIMEType = "image/png"
	token := "@" + name
	id := a.attachSeq
	a.attachSeq++
	a.attachments[id] = &attachment{
		id: id, text: token, raw: name,
		state: attachSafe, sentinel: rolemanager.SentinelSafe,
		img: &img, meta: meta,
	}
	a.attachOrder = append(a.attachOrder, id)
	a.insertIntoComposer(token + " ")
	a.relayout()
	return nil
}

// insertIntoComposer adds text at the cursor.
func (a *App) insertIntoComposer(s string) {
	off := a.editor.CursorOffset()
	a.editor.ReplaceRange(off, off, s)
}

// pastedImageToken turns a pasted or dragged file path into an @ token when it
// names an existing PNG or JPEG file. A terminal delivers a dropped file as a
// paste of its path, often quoted or with backslash-escaped spaces. Anything
// else (several lines, text that is not a path, a file that is not an image
// or is not there) is left alone and pastes as text.
func pastedImageToken(pasted string) (string, bool) {
	p := strings.TrimSpace(pasted)
	if p == "" || strings.ContainsAny(p, "\n\r\x00") {
		return "", false
	}
	if n := len(p); n >= 2 && (p[0] == '\'' || p[0] == '"') && p[n-1] == p[0] {
		p = p[1 : n-1]
	}
	p = strings.ReplaceAll(p, `\ `, " ")
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	}
	p = expandHomePath(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(p), ".")) {
	case "png", "jpg", "jpeg":
	default:
		return "", false
	}
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	if strings.ContainsAny(p, " \t\"") {
		return `@"` + strings.ReplaceAll(p, `"`, "") + `"`, true
	}
	return "@" + p, true
}

// pastedPathKey handles a bracketed paste. A dropped image file becomes an @
// attachment; any other paste goes on to the editor unchanged.
func (a *App) pastedPathKey(m tea.KeyMsg) (tea.Cmd, bool) {
	if !m.Paste {
		return nil, false
	}
	tok, ok := pastedImageToken(string(m.Runes))
	if !ok {
		return nil, false
	}
	a.insertIntoComposer(tok + " ")
	return a.syncAttachments(), true
}
