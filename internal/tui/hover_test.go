package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/tui/components"
)

// renderFrame renders a.messages into a.lastFrame the way chatView does, so
// the hover hit-tests exercise the same provenance the real frame carries.
func renderFrame(t *testing.T, a *App) {
	t.Helper()
	body, lm := components.MessageList{
		Messages:  a.messages,
		Width:     a.contentWidth(),
		ExpandAll: a.expandAll,
		ShowTools: true,
	}.Render()
	a.lastBody = body
	a.lastFrame = frame{
		lines:   lm,
		top:     a.headerHeight(),
		left:    a.contentLeft(),
		width:   a.contentWidth(),
		height:  24,
		yOffset: 0,
	}
}

// hoverLine finds the first selectable line with the given owner and flags and
// returns its content-line index.
func hoverLine(t *testing.T, a *App, owner int, file, collapsed bool) int {
	t.Helper()
	for i, l := range a.lastFrame.lines {
		if l.Owner == owner && l.File == file && l.Collapsed == collapsed && !l.Chrome && l.Width > 0 {
			return i
		}
	}
	t.Fatalf("no line with owner %d file=%v collapsed=%v in %+v", owner, file, collapsed, a.lastFrame.lines)
	return 0
}

// pointAt sets the recorded pointer over the middle of the given content line.
func pointAt(a *App, line int) {
	l := a.lastFrame.lines[line]
	a.mouseX = a.lastFrame.left + l.Col + l.Width/2
	a.mouseY = a.lastFrame.top + line
	a.mousePresent = true
}

func TestRecomputeHoverFilePanel(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
			Meta: map[string]any{"path": "main.go"}, Content: "package main\n\nfunc main() {}\n"},
	}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, true, false))
	a.recomputeHover()

	if !a.hover.file || a.hover.msg != 0 {
		t.Fatalf("hover = %+v, want file panel 0", a.hover)
	}
	if a.hover.collapsed || a.hover.session {
		t.Fatalf("hover = %+v, want file only", a.hover)
	}
	hint := a.hoverHint()
	for _, want := range []string{"ctrl+s", "save main.go", "ctrl+c", "copy"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
}

func TestRecomputeHoverCollapsedPanel(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"seq 5"}`, Content: "l1\nl2\nl3\nl4\nl5", Status: "✓"},
	}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, false, true))
	a.recomputeHover()

	if !a.hover.collapsed || a.hover.msg != 0 {
		t.Fatalf("hover = %+v, want collapsed panel 0", a.hover)
	}
	if a.hover.file || a.hover.session {
		t.Fatalf("hover = %+v, want collapsed only", a.hover)
	}
	if hint := a.hoverHint(); !strings.Contains(hint, "ctrl+o") || !strings.Contains(hint, "expand all") {
		t.Fatalf("hint = %q, want ctrl+o expand all", hint)
	}
}

func TestRecomputeHoverCollapsedFilePanelOffersBoth(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
			Meta:    map[string]any{"path": "main.go"},
			Content: "package main\n\nimport \"fmt\"\n\nfunc main() {}\n"},
	}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, true, true))
	a.recomputeHover()

	if !a.hover.file || !a.hover.collapsed {
		t.Fatalf("hover = %+v, want file and collapsed", a.hover)
	}
	hint := a.hoverHint()
	for _, want := range []string{"ctrl+s", "ctrl+c", "ctrl+o"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
}

func TestRecomputeHoverSession(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.sessionID = "abcd1234abcd1234"
	a.mode = "agent"
	a.refreshFooter()
	col, w, ok := a.footer.SessionSpan()
	if !ok {
		t.Fatal("expected a session span")
	}
	row := a.height - 1 - a.footerHeight() + 2
	a.mousePresent = true
	a.mouseX = a.contentLeft() + col + w/2
	a.mouseY = row
	a.recomputeHover()

	if !a.hover.session {
		t.Fatalf("hover = %+v, want session", a.hover)
	}
	if hint := a.hoverHint(); !strings.Contains(hint, "ctrl+x") || !strings.Contains(hint, "copy session id") {
		t.Fatalf("hint = %q, want ctrl+x copy session id", hint)
	}
}

func TestRecomputeHoverNone(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	// A tool row with no output yet has no text to hand out.
	a.messages = []components.Message{{Role: "tool", ToolName: "Read"}}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, false, false))
	a.recomputeHover()

	if a.hover.text {
		t.Fatalf("hover = %+v, want no copyable text", a.hover)
	}
	if a.hoverHint() != "" {
		t.Fatalf("hint = %q, want empty", a.hoverHint())
	}
}

func TestHitSessionGeometry(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.sessionID = "abcd1234abcd1234"
	a.mode = "agent"
	a.refreshFooter()

	col, w, ok := a.footer.SessionSpan()
	if !ok {
		t.Fatal("expected a session span")
	}
	row := a.height - 1 - a.footerHeight() + 2
	if !a.hitSession(a.contentLeft()+col+1, row) {
		t.Fatalf("hitSession at (%d,%d) should hit", a.contentLeft()+col+1, row)
	}
	if !a.hitSession(a.contentLeft()+col+w-1, row) {
		t.Fatalf("hitSession at the session's last cell should hit")
	}
	if a.hitSession(0, row) {
		t.Fatalf("hitSession far left should miss")
	}
	if a.hitSession(a.contentLeft()+col, a.height-1) {
		t.Fatalf("hitSession outside the footer should miss")
	}
}

// hoverFileApp builds a chat app with one Read message and hover over it.
func hoverFileApp(t *testing.T) *App {
	t.Helper()
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
			Meta: map[string]any{"path": "main.go"}, Content: "package main\n\nfunc main() {}\n"},
	}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, true, false))
	a.recomputeHover()
	return a
}

func TestCtrlSStartsSaveFileOnlyWhenHoveringPanel(t *testing.T) {
	a := hoverFileApp(t)
	if cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS}); cmd != nil {
		t.Fatalf("ctrl+s returned %#v, want nil", cmd)
	}
	if !a.saveFileMode || a.saveFileMsg != 0 {
		t.Fatalf("ctrl+s should open save-file flow: mode=%v msg=%d", a.saveFileMode, a.saveFileMsg)
	}

	// An assistant panel with text enters the flow too.
	b := New(Options{Workdir: t.TempDir()})
	b.width = 120
	b.height = 40
	b.messages = []components.Message{{Role: "assistant", Content: "a long reply"}}
	renderFrame(t, b)
	pointAt(b, hoverLine(t, b, 0, false, false))
	b.recomputeHover()
	if cmd := b.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS}); cmd != nil {
		t.Fatalf("ctrl+s over an assistant panel returned %#v", cmd)
	}
	if !b.saveFileMode {
		t.Fatal("ctrl+s over an assistant panel must open the save-file flow")
	}

	// A running tool row with no output does not.
	c := New(Options{Workdir: t.TempDir()})
	c.width = 120
	c.height = 40
	c.messages = []components.Message{{Role: "tool", ToolName: "Read"}}
	renderFrame(t, c)
	pointAt(c, hoverLine(t, c, 0, false, false))
	c.recomputeHover()
	if cmd := c.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS}); cmd != nil {
		t.Fatalf("ctrl+s over an empty tool row returned %#v", cmd)
	}
	if c.saveFileMode {
		t.Fatal("ctrl+s over an empty tool row must not open the save-file flow")
	}
}

func TestSaveFileWritesRelativePath(t *testing.T) {
	a := hoverFileApp(t)
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	a.editor.SetValue("out/main.go")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.saveFileMode {
		t.Fatal("save-file flow should be closed after enter")
	}
	got, err := os.ReadFile(filepath.Join(a.workdir, "out", "main.go"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(got) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("saved content = %q", got)
	}
}

func TestSaveFileWritesAbsolutePath(t *testing.T) {
	a := hoverFileApp(t)
	dest := filepath.Join(t.TempDir(), "saved.go")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	a.editor.SetValue(dest)
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read absolute saved file: %v", err)
	}
	if string(got) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("saved content = %q", got)
	}
}

func TestSaveFileEmptyPathCancels(t *testing.T) {
	a := hoverFileApp(t)
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	a.editor.SetValue("   ")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.saveFileMode {
		t.Fatal("empty path must cancel the save-file flow")
	}
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "path required") {
		t.Fatalf("last system message = %q, want path required", last.Text())
	}
}

func TestSaveFileEscCancels(t *testing.T) {
	a := hoverFileApp(t)
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	a.editor.SetValue("out/main.go")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})

	if a.saveFileMode {
		t.Fatal("esc must cancel the save-file flow")
	}
	if _, err := os.Stat(filepath.Join(a.workdir, "out", "main.go")); !os.IsNotExist(err) {
		t.Fatalf("esc must not write the file: %v", err)
	}
}

func TestCtrlCOnFileCopiesFileNotPrompt(t *testing.T) {
	a := hoverFileApp(t)
	a.editor.SetValue("the prompt")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c over a file panel should copy")
	}
	msg := cmd()
	copied, ok := msg.(copiedMsg)
	if !ok {
		t.Fatalf("ctrl+c produced %#v, want copiedMsg", msg)
	}
	if !strings.Contains(copied.text, "copied file to clipboard") {
		t.Fatalf("copy feedback = %q, want file copy", copied.text)
	}
}

// hoverAssistantApp builds a chat app with one assistant reply and hover over
// it.
func hoverAssistantApp(t *testing.T) *App {
	t.Helper()
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{{Role: "assistant", Content: "the full reply"}}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, false, false))
	a.recomputeHover()
	return a
}

func TestCtrlCOnAssistantCopiesReplyNotPrompt(t *testing.T) {
	a := hoverAssistantApp(t)
	a.editor.SetValue("the prompt")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c over an assistant panel should copy")
	}
	msg := cmd()
	copied, ok := msg.(copiedMsg)
	if !ok {
		t.Fatalf("ctrl+c produced %#v, want copiedMsg", msg)
	}
	if !strings.Contains(copied.text, "copied file to clipboard") {
		t.Fatalf("copy feedback = %q, want panel copy", copied.text)
	}
}

func TestHoverHintOverAssistantPanel(t *testing.T) {
	a := hoverAssistantApp(t)
	hint := a.hoverHint()
	for _, want := range []string{"ctrl+s", "ctrl+c", "copy"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
}

func TestHoverSaveName(t *testing.T) {
	a := hoverFileApp(t)
	if got := a.hoverSaveName(); got != "main.go" {
		t.Fatalf("file panel save name = %q, want main.go", got)
	}

	b := hoverAssistantApp(t)
	b.sessionID = "abcd1234abcd1234"
	if got := b.hoverSaveName(); got != "signet-abcd1234-0.md" {
		t.Fatalf("assistant save name = %q, want signet-abcd1234-0.md", got)
	}

	// Tool panels use .txt.
	c := New(Options{Workdir: t.TempDir()})
	c.width = 120
	c.height = 40
	c.sessionID = "abcd1234abcd1234"
	c.messages = []components.Message{{Role: "tool", ToolName: "Bash", Content: "output"}}
	renderFrame(t, c)
	pointAt(c, hoverLine(t, c, 0, false, false))
	c.recomputeHover()
	if got := c.hoverSaveName(); got != "signet-abcd1234-0.txt" {
		t.Fatalf("tool save name = %q, want signet-abcd1234-0.txt", got)
	}

	// No session id drops the segment.
	d := hoverAssistantApp(t)
	d.sessionID = ""
	if got := d.hoverSaveName(); got != "signet-0.md" {
		t.Fatalf("no-session save name = %q, want signet-0.md", got)
	}

	// Long names truncate at 40 runes.
	e := hoverAssistantApp(t)
	e.sessionID = "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"
	if got := e.hoverSaveName(); len([]rune(got)) > 40 {
		t.Fatalf("save name too long: %q", got)
	}
}

func TestCtrlCWithoutFileStillCopiesPrompt(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("hello")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c should copy the prompt")
	}
	msg := cmd()
	if _, ok := msg.(copiedMsg); !ok {
		t.Fatalf("ctrl+c produced %#v, want copiedMsg", msg)
	}
}

func TestCtrlXCopiesSessionID(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.sessionID = "abcd1234abcd1234"
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd == nil {
		t.Fatal("ctrl+x should copy the session id")
	}
	msg := cmd()
	copied, ok := msg.(copiedMsg)
	if !ok {
		t.Fatalf("ctrl+x produced %#v, want copiedMsg", msg)
	}
	if !strings.Contains(copied.text, "copied session id to clipboard") {
		t.Fatalf("copy feedback = %q, want session id copy", copied.text)
	}
}
