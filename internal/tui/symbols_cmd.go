package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// symbolsTimeout bounds one /symbols call, a build included.
const symbolsTimeout = 20 * time.Second

// symbolsUsage is what /symbols prints for an argument it cannot use.
const symbolsUsage = "usage: /symbols (index status), /symbols <name> (where a name is declared), /symbols <path> (a file's declarations), /symbols refresh"

// symbolsMsg is a finished /symbols call. sessionID is the session it was asked
// in; an answer that arrives after the session changed is dropped.
type symbolsMsg struct {
	sessionID string
	text      string
}

// symbolsCommand is /symbols (docs/symbol-map.md): the user's view of the
// symbol index. The answer is drawn by the same tools a model calls, over the
// same source, so the user is never shown more than a model would be, and it
// is written to the screen only: an ephemeral row that never reaches the
// session record, a model turn or sync. No model can start it.
func (a *App) symbolsCommand(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if !a.settings.SymbolIndexEnabled() {
		a.addEphemeralSystem("symbols: the symbol index is off (symbol_index.enabled)")
		return nil
	}
	set := a.settings
	workdir := a.workdir
	var src tools.SymbolSource
	if a.agent != nil {
		src = a.agent.SymbolSource()
	}
	if src == nil {
		perms := permissions.From(set.Permissions.Allow, set.Permissions.Ask, set.Permissions.Deny)
		src = agent.NewSymbolSource(workdir, set, perms)
	}
	sid := a.sessionID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), symbolsTimeout)
		defer cancel()
		return symbolsMsg{sessionID: sid, text: symbolsAnswer(ctx, workdir, set, src, arg)}
	}
}

// handleSymbols shows a finished /symbols call.
func (a *App) handleSymbols(m symbolsMsg) tea.Cmd {
	if m.sessionID != a.sessionID {
		return nil
	}
	a.addEphemeralSystem(m.text)
	return nil
}

// symbolsAnswer builds the text of one /symbols call.
func symbolsAnswer(ctx context.Context, workdir string, set config.Settings, src tools.SymbolSource, arg string) string {
	switch strings.ToLower(arg) {
	case "", "status":
		return symbolsStats(agent.SymbolIndexStats(ctx, workdir, set))
	case "refresh":
		return symbolsStats(agent.RefreshSymbolIndex(ctx, workdir, set))
	}
	hub := &tools.SymbolHub{}
	hub.Set(src)
	var (
		res tools.Result
		err error
	)
	if info, statErr := os.Lstat(filepath.Join(workdir, filepath.FromSlash(arg))); statErr == nil && info.Mode().IsRegular() {
		res, err = (&tools.Outline{Root: workdir, Cwd: tools.NewCwd(workdir), Hub: hub}).Execute(ctx, map[string]any{"file_path": arg})
	} else {
		res, err = (&tools.Symbol{Hub: hub, MaxResults: set.SymbolIndexMaxResults()}).Execute(ctx, map[string]any{"name": arg})
	}
	if err != nil {
		return "symbols: " + sanitize.Line(err.Error(), 300) + "\n" + symbolsUsage
	}
	return sanitize.Text(res.Content)
}

// symbolsStats renders an index size, or says it is unavailable.
func symbolsStats(st interface{ String() string }, ok bool) string {
	if !ok {
		return "symbols: the index is unavailable (the walk or a parse ran out of time); Grep still searches every file"
	}
	return "symbols: " + st.String()
}
