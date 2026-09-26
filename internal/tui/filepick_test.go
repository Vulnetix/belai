package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// setFileList gives the App a cached workspace listing so the chooser can be
// exercised without shelling out to Glob.
func setFileList(a *App, files ...string) {
	a.files = files
	a.filesLoadedAt = time.Now()
	a.filesLoading = false
}

// typeString feeds runes through handleChatKey and returns the updated App.
func typeString(a *App, s string) *App {
	for _, r := range s {
		m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		a = m.(*App)
	}
	return a
}

// moveLeft sends left-arrow keystrokes.
func moveLeft(a *App, n int) *App {
	for i := 0; i < n; i++ {
		m, _ := a.Update(tea.KeyMsg{Type: tea.KeyLeft})
		a = m.(*App)
	}
	return a
}

func TestFilePrefixAtCursorMidPrompt(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a = typeString(a, "review @intern notes")
	// Cursor is at the end; put it after "review @intern" and before " notes".
	a = moveLeft(a, 6)
	at, prefix, ok := a.filePrefix()
	if !ok {
		t.Fatalf("expected a file prefix")
	}
	if prefix != "intern" {
		t.Fatalf("prefix = %q, want intern", prefix)
	}
	if at != 7 {
		t.Fatalf("@ offset = %d, want 7", at)
	}
}

func TestFilePrefixSkipsAgentScheme(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a = typeString(a, "ask @agent:security")
	at, prefix, ok := a.filePrefix()
	if !ok {
		t.Fatalf("expected a file prefix for the trailing token")
	}
	// The prefix returned is the raw text after '@'; the file chooser uses it
	// as a literal filter. @agent: is no longer a special agent-picker prefix
	// in the TUI, but it is still reserved from attachment parsing.
	if prefix != "agent:security" {
		t.Fatalf("prefix = %q, want agent:security", prefix)
	}
	if at <= 0 {
		t.Fatalf("@ offset = %d, want > 0", at)
	}
}

func TestFilePrefixMultiLine(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.editor.SetValue("first line\nsecond @intern")
	a.editor.CursorEnd()
	at, prefix, ok := a.filePrefix()
	if !ok || prefix != "intern" {
		t.Fatalf("prefix = %q, ok=%v, want intern", prefix, ok)
	}
	if at != 18 {
		t.Fatalf("@ offset on second line = %d, want 18", at)
	}
}

func TestFileCandidatesFilter(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	setFileList(a, "internal/tui/app.go", "internal/tui/view.go", "README.md")
	a.editor.SetValue("review @ui")
	a.editor.CursorEnd()
	cands := a.fileCandidates()
	if len(cands) != 2 {
		t.Fatalf("candidates = %v, want 2", cands)
	}
}

func TestFilePickerFiltersImageExtensions(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	setFileList(a, "README.md", "screenshot.png", "photo.jpg", "animation.gif", "doc.webp")
	a.editor.SetValue("see @")
	a.editor.CursorEnd()
	cands := a.fileCandidates()
	if len(cands) != 1 || cands[0] != "README.md" {
		t.Fatalf("candidates = %v, want only README.md", cands)
	}
}

func TestFilePickerAcceptQuotesPathsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	a := NewApp(dir, "")
	if err := os.WriteFile(filepath.Join(dir, "path with spaces.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setFileList(a, "path with spaces.txt", "plain.txt")
	a.editor.SetValue("read @path")
	a.editor.CursorEnd()
	a.fileIndex = 0
	cmd := a.acceptFilePick()
	if cmd == nil {
		t.Fatalf("acceptFilePick should return a sync command (cands=%v idx=%d)", a.fileCandidates(), a.fileIndex)
	}
	got := a.editor.Value()
	want := `read @"path with spaces.txt" `
	if got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestFilePickerAcceptAtCursorMidPrompt(t *testing.T) {
	dir := t.TempDir()
	a := NewApp(dir, "")
	path := filepath.Join(dir, "internal", "tui", "app.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setFileList(a, "internal/tui/app.go")
	a = typeString(a, "before after")
	a = moveLeft(a, 5) // cursor after the space, before "after"
	a = typeString(a, "@app")
	a.fileIndex = 0
	a.acceptFilePick()
	got := a.editor.Value()
	want := "before @internal/tui/app.go after"
	if got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestFilePickerEscDismissesAndTypingReopens(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	setFileList(a, "internal/tui/app.go")
	a = typeString(a, "@i")
	if !a.filePickerVisible() {
		t.Fatalf("picker should be visible")
	}

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)
	if a.filePickerVisible() {
		t.Fatalf("esc should dismiss the picker")
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	a = m.(*App)
	if !a.filePickerVisible() {
		t.Fatalf("typing should reopen the picker")
	}
}

func TestFilePickerAgentSchemeIsNotAgentPicker(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := NewApp(t.TempDir(), "")
	a.mode = "agent"
	setFileList(a, "internal/agent/agent.go")
	a.editor.SetValue("@agent:")
	a.editor.CursorEnd()
	// @agent: no longer opens the agent picker; it is an ordinary file-chooser
	// prefix that happens not to match any file here.
	if a.agentPickerVisible() {
		t.Fatalf("agent picker should not show for @agent:")
	}
}

func TestFilePickerWindowScrollsPastFive(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	files := []string{
		"a/one.go", "a/two.go", "a/three.go", "a/four.go", "a/five.go",
		"a/six.go", "a/seven.go", "a/eight.go",
	}
	setFileList(a, files...)
	a.editor.SetValue("@a")
	a.editor.CursorEnd()
	a.fileIndex = 6
	a.renderFilePicker()
	if a.fileScroll <= 0 {
		t.Fatalf("fileScroll = %d, want > 0 when cursor is below the window", a.fileScroll)
	}
	visible := a.fileCandidates()[a.fileScroll : a.fileScroll+pickerRows]
	if visible[len(visible)-1] != "a/seven.go" {
		t.Fatalf("window ends at %q, want a/seven.go", visible[len(visible)-1])
	}
}

func TestFilePickerCycleWraps(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	setFileList(a, "a.go", "b.go", "c.go")
	a.editor.SetValue("@")
	a.editor.CursorEnd()
	a.cycleFile(-1)
	if got, _ := a.selectedFile(); got != "c.go" {
		t.Fatalf("up from top should wrap to last, got %q", got)
	}
	a.cycleFile(1)
	if got, _ := a.selectedFile(); got != "a.go" {
		t.Fatalf("down from wrapped position should land on first, got %q", got)
	}
}

func TestFilePickerRenderContainsCounter(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	setFileList(a, "a.go", "b.go", "c.go")
	a.editor.SetValue("@")
	a.editor.CursorEnd()
	a.fileIndex = 1
	rendered := a.renderFilePicker()
	if !strings.Contains(rendered, "2/3") {
		t.Fatalf("rendered picker missing counter: %q", rendered)
	}
	if !strings.Contains(rendered, "b.go") {
		t.Fatalf("rendered picker missing highlighted row: %q", rendered)
	}
}
