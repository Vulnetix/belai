package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/filediff"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// attachState tracks the lifecycle of one @file attachment.
type attachState int

const (
	attachResolving attachState = iota
	attachClassifying
	attachSafe
	attachRejected
	// attachNeedsRoot is an @path outside every session root, waiting for the
	// user to adopt rootDir as a root (or decline) before it is read.
	attachNeedsRoot
)

// attachment is one parsed @file reference.
type attachment struct {
	id       int
	text     string // the literal "@path" the user typed, used as join key
	raw      string // path part after sanitisation
	root     string // confinement root the path resolved against (default workdir)
	body     string // file contents (or directory listing) after successful validation
	state    attachState
	reason   string
	diff     filediff.Change // worktree-vs-index change, computed on validation
	sentinel rolemanager.Sentinel
	isDir    bool   // the target is a directory: listed, not read
	rootDir  string // attachNeedsRoot: the directory proposed as a new root
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
	diff     filediff.Change
	isDir    bool
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
		if cmd := a.resolveAttachment(att); cmd != nil {
			cmds = append(cmds, cmd)
		}
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

// resolveAttachment resolves one complete attachment against the session
// roots. Inside a root it starts validation and returns that command. An
// existing path outside every root is parked in attachNeedsRoot for the
// user's confirmation, provided its directory could become a root at all;
// anything else is rejected.
func (a *App) resolveAttachment(att *attachment) tea.Cmd {
	root, rel, err := a.resolveAttachmentPath(att.raw)
	if err == nil {
		att.root = root
		att.raw = rel
		att.state = attachClassifying
		att.rootDir = ""
		return a.validateAttachmentCmd(att.id, root, rel)
	}
	att.state = attachRejected
	att.reason = err.Error()
	target, ok := a.outsideRootTarget(att.raw)
	if !ok {
		return nil
	}
	dir := target
	if info, statErr := os.Stat(target); statErr == nil && !info.IsDir() {
		dir = filepath.Dir(target)
	}
	abs, checkErr := a.rootSet().CheckRoot(dir)
	if checkErr != nil {
		att.reason = fmt.Sprintf("%s: path escapes the session roots (%v); choose a subdirectory", att.text, checkErr)
		return nil
	}
	att.state = attachNeedsRoot
	att.reason = ""
	att.rootDir = abs
	return nil
}

// outsideRootTarget returns the absolute, symlink-resolved form of an @path
// that exists and lies outside every session root. Relative paths are read
// from the primary workdir, the way the @ chooser spells them.
func (a *App) outsideRootTarget(raw string) (string, bool) {
	abs := expandHomePath(raw)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.workdir, abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	if a.rootSet().Contains(resolved) {
		return "", false
	}
	return resolved, true
}

// rootSet returns the session's confinement roots as a Cwd: the primary
// workdir plus every workspace directory that is a valid root. It mirrors
// buildAgentSession, so the @ chooser and @file resolution agree with what
// the tools will accept — a persisted directory the Cwd refuses (for example
// an ancestor of the workdir) is not a root here either.
func (a *App) rootSet() *tools.Cwd {
	c := tools.NewCwd(a.workdir)
	for _, d := range a.workspaceDirs {
		_ = c.AddRoot(d)
	}
	return c
}

// expandHomePath resolves a leading "~/" (or a bare "~") against the user's
// home directory, leaving any other path untouched.
func expandHomePath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// pendingRootConfirm returns the first attachment waiting on root adoption.
// Only one confirmation is on screen at a time; the rest follow in order.
func (a *App) pendingRootConfirm() (*attachment, bool) {
	for _, id := range a.attachOrder {
		if att, ok := a.attachments[id]; ok && att.state == attachNeedsRoot {
			return att, true
		}
	}
	return nil, false
}

// rootConfirmVisible reports whether the root confirmation pane is drawn.
func (a *App) rootConfirmVisible() bool {
	if a.view != viewChat {
		return false
	}
	_, ok := a.pendingRootConfirm()
	return ok
}

// handleRootConfirmKey answers the root confirmation. It swallows every key
// it does not handle, so the prompt cannot be typed past: s adopts the
// directory for this session, p also saves it for the project exactly like
// /add-dir, and n or esc declines and withholds the attachment.
func (a *App) handleRootConfirmKey(m tea.KeyMsg) tea.Cmd {
	att, ok := a.pendingRootConfirm()
	if !ok {
		return nil
	}
	switch strings.ToLower(m.String()) {
	case "s":
		return a.addWorkspaceDirCmd(att.rootDir, false)
	case "p":
		return a.addWorkspaceDirCmd(att.rootDir, true)
	case "n", "esc":
		att.state = attachRejected
		att.reason = att.text + ": outside the session roots (declined)"
		a.relayout()
		return a.flushPendingSubmit()
	}
	return nil
}

// settleRootConfirm re-resolves the attachments that were waiting on dir once
// it has been adopted (err nil) or refused (err set).
func (a *App) settleRootConfirm(dir string, err error) tea.Cmd {
	var cmds []tea.Cmd
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att == nil || att.state != attachNeedsRoot {
			continue
		}
		if err != nil {
			if att.rootDir == dir {
				att.state = attachRejected
				att.reason = att.text + ": " + err.Error()
			}
			continue
		}
		if cmd := a.resolveAttachment(att); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	a.relayout()
	cmds = append(cmds, a.flushPendingSubmit())
	return tea.Batch(cmds...)
}

// rootConfirmHeight returns the rendered height of the confirmation pane.
func (a *App) rootConfirmHeight() int {
	if !a.rootConfirmVisible() {
		return 0
	}
	return lipgloss.Height(a.renderRootConfirm())
}

// renderRootConfirm draws the amber confirmation pane for adopting a root.
func (a *App) renderRootConfirm() string {
	att, ok := a.pendingRootConfirm()
	if !ok {
		return ""
	}
	return components.Panel{
		Title: "confirm root",
		Body: components.WarnStyle.Render("⚠ ") +
			components.EmphStyle.Render(att.rootDir) +
			components.MutedStyle.Render(" is outside the session roots. Add it as a root?\n") +
			components.MutedStyle.Render("s: this session  p: save for project  n: no"),
		Width:  a.contentWidth(),
		Accent: lipgloss.TerminalColor(components.ColorAmber),
	}.View()
}

// resolveAttachmentPath resolves an @file path against the primary workdir
// and any added workspace directories. It returns the matching root and the
// root-relative path. Absolute paths that prefix-match a root are handled
// directly; relative paths are sanitized against each root in turn.
func (a *App) resolveAttachmentPath(raw string) (root, rel string, err error) {
	roots := append([]string{a.workdir}, a.rootSet().Roots()[1:]...)
	for _, r := range roots {
		rel, err = sanitizeAgainstRoot(r, raw)
		if err == nil {
			return r, rel, nil
		}
	}
	return "", "", err
}

// sanitizeAgainstRoot returns the root-relative form of raw, accepting both
// absolute paths that sit under root and relative paths confined to root.
func sanitizeAgainstRoot(root, raw string) (string, error) {
	if filepath.IsAbs(raw) {
		if raw == root {
			return ".", nil
		}
		sep := string(filepath.Separator)
		if prefix := root + sep; strings.HasPrefix(raw, prefix) {
			rel := strings.TrimPrefix(raw, prefix)
			if rel == "." || rel == "" || strings.Contains(rel, "..") {
				return "", fmt.Errorf("path escapes root")
			}
			return rel, nil
		}
	}
	return tools.SanitizePath(root, raw)
}

// validateAttachmentCmd reads and classifies one file in a goroutine. It
// captures TUI-derived values into locals so the closure does not race.
func (a *App) validateAttachmentCmd(id int, root, rel string) tea.Cmd {
	cfg := a.cfg
	client := a.client
	pol := a.effectivePosture()
	traceCtx := a.toolContext(context.Background(), "Read", "")
	return func() tea.Msg {
		ctx := traceCtx
		// A directory is listed, not read — the answer an Ls call would give.
		// Entry names are shaped, harness-known output (one per line), so the
		// listing is sanitised and admitted without a classifier round trip,
		// exactly like an LS result: it carries no file content to classify.
		abs := filepath.Join(root, rel)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			body, err := listDirForAttachment(abs)
			if err != nil {
				return attachValidatedMsg{id: id, err: err, sentinel: rolemanager.SentinelMalformed, isDir: true}
			}
			return attachValidatedMsg{id: id, body: sanitize.Sanitize(body), sentinel: rolemanager.SentinelSafe, isDir: true}
		}
		// Verbatim: the body is diffed against the index and handed over as the
		// file itself, so it must not carry the model-facing gutter or trailer.
		read := &tools.Read{Root: root, MaxBytes: 64 * 1024, Verbatim: true}
		res, err := read.Execute(ctx, map[string]any{"path": rel})
		if err != nil {
			return attachValidatedMsg{id: id, err: err, sentinel: rolemanager.SentinelMalformed}
		}
		// Compute the worktree-vs-index diff on the validation goroutine so the
		// Update loop never shells out to git.
		rec := filediff.NewRecorder(root)
		diff := rec.WorktreeChange(ctx, rel, res.Content)
		// With the gate ignored the verdict cannot change the outcome, so the
		// classifier is not called at all. Calling it and then discarding the
		// answer would spend a round trip per attachment and send the file to
		// the provider's classifier turn, which is the opposite of what
		// turning guardrails off asks for. Sanitising still runs.
		if pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			return attachValidatedMsg{id: id, body: sanitize.Sanitize(res.Content), sentinel: rolemanager.SentinelSafe, diff: diff}
		}
		pipe := run.NewPipeline(cfg, client, a.cache)
		dec, perr := pipe.Process(ctx, res)
		body := res.Content
		if perr == nil && dec.Action == rolemanager.ActionProceed {
			body = dec.Content
		}
		return attachValidatedMsg{id: id, body: body, err: perr, sentinel: dec.Sentinel, diff: diff}
	}
}

// attachListCap bounds a directory listing at the native tools' output cap,
// so a ten-thousand-entry directory cannot flood the turn.
const attachListCap = 64 * 1024

// listDirForAttachment renders one directory the way the model should see it:
// entries sorted by name, one per line, subdirectories marked with a trailing
// slash. Past the cap the listing stops at a line boundary with a marker
// rather than cutting a name mid-string. An empty directory answers
// explicitly, because a zero-byte attachment would be dropped from the turn
// while its preview row still promised a listing.
func listDirForAttachment(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "(empty directory)\n", nil
	}
	var b strings.Builder
	truncated := false
	for _, e := range entries {
		line := e.Name()
		if e.IsDir() {
			line += "/"
		}
		if b.Len()+len(line)+1 > attachListCap {
			truncated = true
			break
		}
		b.WriteString(line + "\n")
	}
	if truncated {
		b.WriteString("… (truncated)\n")
	}
	return b.String(), nil
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
	att.sentinel = m.sentinel
	att.diff = m.diff
	att.isDir = m.isDir
	if m.err != nil {
		att.state = attachRejected
		att.reason = m.err.Error()
	} else if m.sentinel.IsSafe() || a.effectivePosture().Level(posture.ToolResultUnsafe) == posture.Ignore {
		att.body = m.body
		att.state = attachSafe
	} else {
		att.state = attachRejected
		att.reason = fmt.Sprintf("%s: classified %s", att.text, m.sentinel.Label())
	}
	a.relayout()
	return a.flushPendingSubmit()
}

// flushPendingSubmit sends a held prompt once every attachment is safe.
// Rejected attachments are skipped (the literal @token remains in the prompt)
// and reported to the model through a sealed directive.
func (a *App) flushPendingSubmit() tea.Cmd {
	if a.pendingInput == "" {
		return nil
	}
	for _, id := range a.attachOrder {
		switch a.attachments[id].state {
		case attachResolving, attachClassifying, attachNeedsRoot:
			return nil
		}
	}
	input := a.pendingInput
	a.pendingInput = ""
	previews, directive := a.attachmentPreviews()
	a.messages = append(a.messages, previews...)
	var atts []run.Attachment
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att.state == attachSafe && att.body != "" {
			kind := "file"
			if att.isDir {
				kind = "directory"
			}
			atts = append(atts, run.Attachment{Kind: kind, Label: att.text, Body: att.body})
		}
	}
	a.attachments = map[int]*attachment{}
	a.attachOrder = nil
	a.editor.Reset()
	a.clearAutocomplete()
	return a.sendWithAttachments(input, atts, directive)
}

func (a *App) sendWithAttachments(input string, atts []run.Attachment, directive string) tea.Cmd {
	turns := a.buildTurns()
	turns = append(turns, run.Turn{Role: "user", Content: input, Attachments: atts, Directive: directive})
	a.messages = append(a.messages, components.Message{Role: "user", Content: input})
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: input})
	return a.send(turns)
}

// attachmentPreviews builds the transcript rows that preview every attachment
// and the directive that tells the model about rejected ones. Safe attachments
// render as Read tool rows with an optional worktree diff; rejected ones
// render as red "withheld" rows.
func (a *App) attachmentPreviews() ([]components.Message, string) {
	var previews []components.Message
	var withheld []string
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		toolName := "Read"
		if att.isDir {
			toolName = "Ls"
		}
		switch att.state {
		case attachSafe:
			meta := map[string]any{"path": att.raw}
			if !att.isDir {
				meta["start_line"] = 1
			}
			msg := components.Message{
				Role:     "tool",
				ToolName: toolName,
				ToolArgs: `{"path":"` + att.raw + `"}`,
				Content:  att.body,
				Meta:     meta,
				Status:   "✓",
			}
			if !att.isDir && !att.diff.Empty() {
				msg.SetDiff(&att.diff)
			}
			previews = append(previews, msg)
		case attachRejected:
			previews = append(previews, components.Message{
				Role:     "tool",
				ToolName: toolName,
				ToolArgs: `{"path":"` + att.raw + `"}`,
				Content:  "tool result withheld: attachment " + att.text + " classified " + att.sentinel.Label(),
			})
			if att.text != "" {
				withheld = append(withheld, fmt.Sprintf("%s (%s)", att.text, att.sentinel.Label()))
			}
		}
	}
	var directive string
	if len(withheld) > 0 {
		directive = "Files the user referenced with @ were not admitted: " +
			strings.Join(withheld, "; ") +
			". Their contents are withheld and are not in this turn. Do not ask for them again. " +
			"Answer from what is present, or tell the user what alternative would let you proceed."
	}
	return previews, directive
}

// hasPendingAttachments reports whether any attachment has not yet resolved.
func (a *App) hasPendingAttachments() bool {
	for _, att := range a.attachments {
		if att.state == attachResolving || att.state == attachClassifying || att.state == attachNeedsRoot {
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
		case attachNeedsRoot:
			parts = append(parts, components.WarnStyle.Render("⚠ ")+att.text)
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
