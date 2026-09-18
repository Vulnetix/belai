package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/tui/components"
)

// attachState tracks the lifecycle of one @file attachment.
type attachState int

const (
	attachResolving attachState = iota
	attachClassifying
	attachSafe
	attachRejected
)

// attachment is one parsed @file reference.
type attachment struct {
	id     int
	text   string // the literal "@path" the user typed, used as join key
	raw    string // path part after sanitisation
	body   string // file contents after successful validation
	state  attachState
	reason string
}

// token is one candidate attachment parsed from editor text.
type token struct {
	text     string
	raw      string
	complete bool
	quoted   bool
}

// attachValidatedMsg carries the result of an async attachment classification.
type attachValidatedMsg struct {
	id       int
	body     string
	err      error
	sentinel rolemanager.Sentinel
}

// reservedAttachSchemes lists prefixes that look like schemes but must not be
// treated as attachments. The only one today is `@agent:`.
var reservedAttachSchemes = map[string]bool{"agent": true}

// parseTokens scans s for attachment candidates. Only whitespace-complete
// tokens are enqueued for validation; incomplete trailing tokens are tracked
// but marked complete=false.
func parseTokens(s string) []token {
	var out []token
	i := 0
	nextIsSpace := func(idx int) bool { return idx < len(s) && isSpace(s[idx]) }
	for i < len(s) {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		// Quoted attachment: @"path with spaces"
		if s[i] == '@' && i+1 < len(s) && s[i+1] == '"' {
			i += 2 // skip @"
			pathStart := i
			for i < len(s) && s[i] != '"' {
				i++
			}
			raw := s[pathStart:i]
			if i < len(s) && s[i] == '"' {
				i++ // skip closing quote
			}
			complete := nextIsSpace(i)
			out = append(out, token{text: s[start:i], raw: raw, complete: complete, quoted: true})
			continue
		}
		// Unquoted field.
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		f := s[start:i]
		if !strings.HasPrefix(f, "@") {
			continue
		}
		rest := f[1:]
		if colon := strings.Index(rest, ":"); colon > 0 && reservedAttachSchemes[rest[:colon]] {
			continue
		}
		if rest == "" {
			continue
		}
		complete := nextIsSpace(i)
		out = append(out, token{text: f, raw: rest, complete: complete})
	}
	return out
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// syncAttachments reconciles the parsed tokens with the live attachment map.
// New complete tokens are enqueued for validation; vanished tokens are dropped.
func (a *App) syncAttachments() tea.Cmd {
	seen := map[string]bool{}
	tokens := parseTokens(a.editor.Value())
	for _, tok := range tokens {
		seen[tok.text] = true
	}
	for id, att := range a.attachments {
		if !seen[att.text] {
			delete(a.attachments, id)
		}
	}
	var cmds []tea.Cmd
	for _, tok := range tokens {
		if _, ok := a.attachmentsByText(tok.text); ok {
			continue
		}
		id := a.attachSeq
		a.attachSeq++
		att := &attachment{id: id, text: tok.text, raw: tok.raw}
		a.attachments[id] = att
		a.attachOrder = append(a.attachOrder, id)
		if !tok.complete {
			att.state = attachResolving
			continue
		}
		rel, err := tools.SanitizePath(a.workdir, tok.raw)
		if err != nil {
			att.state = attachRejected
			att.reason = err.Error()
			continue
		}
		att.raw = rel
		att.state = attachClassifying
		cmds = append(cmds, a.validateAttachmentCmd(id, rel))
	}
	// Compact attachOrder to remove deleted ids.
	order := a.attachOrder[:0]
	for _, id := range a.attachOrder {
		if _, ok := a.attachments[id]; ok {
			order = append(order, id)
		}
	}
	a.attachOrder = order
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (a *App) attachmentsByText(text string) (*attachment, bool) {
	for _, att := range a.attachments {
		if att.text == text {
			return att, true
		}
	}
	return nil, false
}

// validateAttachmentCmd reads and classifies one file in a goroutine. It
// captures TUI-derived values into locals so the closure does not race.
func (a *App) validateAttachmentCmd(id int, rel string) tea.Cmd {
	cfg := a.cfg
	client := a.client
	workdir := a.workdir
	pol := a.effectivePosture()
	return func() tea.Msg {
		ctx := context.Background()
		read := &tools.Read{Root: workdir, MaxBytes: 64 * 1024}
		res, err := read.Execute(ctx, map[string]any{"path": rel})
		if err != nil {
			return attachValidatedMsg{id: id, err: err, sentinel: rolemanager.SentinelMalformed}
		}
		// With the gate ignored the verdict cannot change the outcome, so the
		// classifier is not called at all. Calling it and then discarding the
		// answer would spend a round trip per attachment and send the file to
		// the provider's classifier turn, which is the opposite of what
		// turning guardrails off asks for. Sanitising still runs.
		if pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			return attachValidatedMsg{id: id, body: sanitize.Sanitize(res.Content), sentinel: rolemanager.SentinelSafe}
		}
		pipe := run.NewPipeline(cfg, client, a.cache)
		dec, perr := pipe.Process(ctx, res)
		body := res.Content
		if perr == nil && dec.Action == rolemanager.ActionProceed {
			body = dec.Content
		}
		return attachValidatedMsg{id: id, body: body, err: perr, sentinel: dec.Sentinel}
	}
}

// handleAttachValidated applies the async validation result, drops stale
// results, and flushes any pending submit once everything is resolved.
func (a *App) handleAttachValidated(m attachValidatedMsg) tea.Cmd {
	att, ok := a.attachments[m.id]
	if !ok {
		return nil
	}
	if !strings.Contains(a.editor.Value(), att.text) {
		delete(a.attachments, m.id)
		return nil
	}
	if m.err != nil {
		att.state = attachRejected
		att.reason = m.err.Error()
	} else if m.sentinel.IsSafe() || a.effectivePosture().Level(posture.ToolResultUnsafe) == posture.Ignore {
		att.body = m.body
		att.state = attachSafe
	} else {
		att.state = attachRejected
		att.reason = fmt.Sprintf("%s: classified %s", att.text, m.sentinel)
	}
	a.relayout()
	return a.flushPendingSubmit()
}

// flushPendingSubmit sends a held prompt once every attachment is safe.
// Rejected attachments are skipped (the literal @token remains in the prompt).
func (a *App) flushPendingSubmit() tea.Cmd {
	if a.pendingInput == "" {
		return nil
	}
	for _, id := range a.attachOrder {
		switch a.attachments[id].state {
		case attachResolving, attachClassifying:
			return nil
		}
	}
	input := a.pendingInput
	a.pendingInput = ""
	var atts []run.Attachment
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att.state == attachSafe && att.body != "" {
			atts = append(atts, run.Attachment{Kind: "file", Label: att.text, Body: att.body})
		}
	}
	a.attachments = map[int]*attachment{}
	a.attachOrder = nil
	a.editor.Reset()
	a.clearAutocomplete()
	return a.sendWithAttachments(input, atts)
}

func (a *App) sendWithAttachments(input string, atts []run.Attachment) tea.Cmd {
	turns := a.buildTurns()
	turns = append(turns, run.Turn{Role: "user", Content: input, Attachments: atts})
	a.messages = append(a.messages, components.Message{Role: "user", Content: input})
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: input})
	return a.send(turns)
}

// hasPendingAttachments reports whether any attachment has not yet resolved.
func (a *App) hasPendingAttachments() bool {
	for _, att := range a.attachments {
		if att.state == attachResolving || att.state == attachClassifying {
			return true
		}
	}
	return false
}

// hasSafeAttachments reports whether any attachment has been validated SAFE.
func (a *App) hasSafeAttachments() bool {
	for _, att := range a.attachments {
		if att.state == attachSafe {
			return true
		}
	}
	return false
}

// attachStripHeight returns 0–2 rows depending on attachments and warnings.
func (a *App) attachStripHeight() int {
	if len(a.attachments) == 0 {
		return 0
	}
	h := 1
	for _, att := range a.attachments {
		if att.state == attachRejected && att.reason != "" {
			h = 2
			break
		}
	}
	return h
}

// renderAttachStrip draws the attachment status line and, when present, a
// warnings line.
func (a *App) renderAttachStrip() string {
	var parts []string
	spin := a.attachSpin.View()
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		switch att.state {
		case attachResolving, attachClassifying:
			parts = append(parts, spin+" "+att.text)
		case attachSafe:
			parts = append(parts, "✓ "+att.text)
		case attachRejected:
			parts = append(parts, lipgloss.NewStyle().Strikethrough(true).Faint(true).Render(att.text))
		}
	}
	var lines []string
	line := lipgloss.NewStyle().MaxWidth(a.width).Render(strings.Join(parts, "  "))
	if line != "" {
		lines = append(lines, line)
	}
	var reasons []string
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att.state == attachRejected && att.reason != "" {
			reasons = append(reasons, att.reason)
		}
	}
	if len(reasons) > 0 {
		w := lipgloss.NewStyle().Faint(true).MaxWidth(a.width).Render(strings.Join(reasons, "; "))
		lines = append(lines, w)
	}
	return strings.Join(lines, "\n")
}
