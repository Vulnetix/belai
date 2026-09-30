package acp

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
)

// turnSnap is the model-facing history on either side of one finished turn.
type turnSnap struct{ before, after []run.Turn }

// maxTreeRows bounds the tree a reply lists; older rows are summarised.
const maxTreeRows = 80

// commandList is what the editor is told it can type. /tree is the only one:
// it moves within the session, so it needs no second session to open. A fork
// would need session/load, which Belai does not advertise.
var commandList = []any{
	map[string]any{
		"name":        "tree",
		"description": "show this session's branches, or continue from an earlier point",
		"input":       map[string]any{"hint": "entry id from the tree"},
	},
}

// announceCommands sends the slash commands once the editor has the session id.
func (s *Server) announceCommands(ss *acpSession) {
	s.update(ss, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": commandList})
}

// sessionCommand answers /tree in place of a model turn. It reports false for
// any other prompt, which then goes to the agent as usual.
func (s *Server) sessionCommand(_ context.Context, ss *acpSession, text string) (any, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "/tree" {
		return nil, false
	}
	arg := strings.Join(fields[1:], " ")
	say := func(format string, a ...any) {
		s.update(ss, map[string]any{"sessionUpdate": "agent_message_chunk", "content": textContent(fmt.Sprintf(format, a...) + "\n")})
	}
	done := map[string]any{"stopReason": "end_turn"}

	// Hold the session's prompt slot so a prompt cannot start mid-navigation.
	ss.mu.Lock()
	if ss.cancel != nil {
		ss.mu.Unlock()
		say("A prompt is running in this session. Cancel it first.")
		return done, true
	}
	ss.cancel = func() {}
	ss.mu.Unlock()
	defer func() {
		ss.mu.Lock()
		ss.cancel = nil
		ss.mu.Unlock()
	}()

	w := ss.log.Writer()
	if w == nil {
		say("This session keeps no transcript, so it has no tree.")
		return done, true
	}
	entries, err := w.Entries()
	if err != nil {
		say("Could not read the session: %v", err)
		return done, true
	}
	if arg == "" {
		say("%s", renderTree(entries))
		return done, true
	}

	leaf, history, prefill, err := s.treeTarget(ss, entries, arg)
	if err != nil {
		say("/tree: %v", err)
		return done, true
	}
	if w.Branch(leaf) == "" {
		say("/tree: the transcript could not be written, so the session stays where it is.")
		return done, true
	}
	ss.mu.Lock()
	ss.history = append([]run.Turn(nil), history...)
	ss.mu.Unlock()
	s.touch(ss)
	say("Continuing from %s. Conversation only: files on disk are not rolled back.", short(arg, entries))
	if prefill != "" {
		say("Reword and send this prompt again:\n\n> %s", sanitize.Line(prefill, 400))
	}
	return done, true
}

// treeTarget resolves an entry id to where the session continues and the
// model-facing history there. The history comes from the snapshots taken when
// each turn finished, never from the transcript, so what the model sees is
// still the sanitized prompt.
func (s *Server) treeTarget(ss *acpSession, entries []session.Entry, arg string) (leaf string, history []run.Turn, prefill string, err error) {
	e, err := session.FindEntry(entries, arg)
	if err != nil {
		return "", nil, "", err
	}
	leaf, prefill, err = session.Target(e)
	if err != nil {
		return "", nil, "", err
	}
	if leaf == "" {
		return "", nil, "", fmt.Errorf("nothing comes before the first message")
	}
	owner := ""
	chain := session.Path(entries, e.ID)
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Type == "user" {
			owner = chain[i].ID
			break
		}
	}
	ss.mu.Lock()
	sn, ok := ss.snaps[owner]
	ss.mu.Unlock()
	if !ok {
		return "", nil, "", fmt.Errorf("that turn did not finish on this connection, so it cannot be restored; pick another")
	}
	if e.Type == "user" {
		return leaf, sn.before, prefill, nil
	}
	return leaf, sn.after, "", nil
}

// renderTree lists the session's branches as one code block, so transcript
// text cannot be read as markup by the editor. Each row starts with the short
// id that /tree takes.
func renderTree(entries []session.Entry) string {
	rows := session.TreeRows(entries, false)
	if len(rows) == 0 {
		return "No messages in this session yet."
	}
	var b strings.Builder
	b.WriteString("Session tree (● here, • on this branch, ○ other branches, ┬ a fork). Use /tree <id> to continue from a row.\n\n```\n")
	skipped := 0
	if len(rows) > maxTreeRows {
		skipped = len(rows) - maxTreeRows
		rows = rows[skipped:]
		fmt.Fprintf(&b, "... %d earlier rows\n", skipped)
	}
	for _, r := range rows {
		glyph := "○"
		switch {
		case r.Leaf:
			glyph = "●"
		case r.OnPath:
			glyph = "•"
		}
		if r.Fork {
			glyph = "┬"
		}
		label := strings.ReplaceAll(r.Label, "`", "'")
		fmt.Fprintf(&b, "%s%s %s  %s\n", strings.Repeat("  ", min(r.Depth, 12)), glyph, r.Entry.ID[:min(8, len(r.Entry.ID))], label)
	}
	b.WriteString("```")
	return b.String()
}

// short is the id as the user typed it, or its first eight characters.
func short(arg string, entries []session.Entry) string {
	if e, err := session.FindEntry(entries, arg); err == nil && len(e.ID) > 8 {
		return e.ID[:8]
	}
	return arg
}
