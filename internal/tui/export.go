package tui

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/session"
)

// exportDoneMsg carries the result of an async session export back to the UI
// loop.
type exportDoneMsg struct {
	path string
	err  error
}

// exportSessionCmd exports a session to <workdir>/.vulnetix/exports/<id>.md.
// The current session is flushed first so an export always reflects everything
// persisted so far. The disk work runs off the UI loop as a tea.Cmd.
func (a *App) exportSessionCmd(arg string) tea.Cmd {
	a.persistTail()

	// Snapshots taken on the UI loop: the goroutine never reads App fields.
	store := a.store
	workdir := a.workdir
	sessionKey := a.sessionKey
	currentID := a.sessionID

	return func() tea.Msg {
		key, id, err := resolveExportSession(store, workdir, sessionKey, currentID, arg)
		if err != nil {
			return exportDoneMsg{err: err}
		}
		entries, err := store.ReadFrom(key, id)
		if err != nil {
			return exportDoneMsg{err: err}
		}
		md := session.ExportMarkdown(entries, session.ExportOptions{ID: id})
		dir := config.ProjectExportsDir(workdir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return exportDoneMsg{err: err}
		}
		path := filepath.Join(dir, id+".md")
		if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
			return exportDoneMsg{err: err}
		}
		return exportDoneMsg{path: path}
	}
}

// resolveExportSession maps an empty argument to the current session and any
// other argument through ResolveAnywhere, exactly like resumeByID.
func resolveExportSession(store *session.Store, workdir string, currentKey session.Key, currentID, arg string) (session.Key, string, error) {
	arg = strings.TrimSpace(arg)
	cur := currentKey
	if cur == "" {
		cur, _ = session.KeyFor(workdir)
	}
	if arg == "" {
		return cur, currentID, nil
	}
	return store.ResolveAnywhere(cur, arg)
}

// handleExportDone reports the export path or the failure as a system line.
func (a *App) handleExportDone(m exportDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("export: " + m.err.Error())
		return nil
	}
	a.addSystem("exported session to " + displayPath(m.path))
	return nil
}
