package components

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func shellMsg(command, out string) Message {
	return Message{Role: ShellRole, ToolArgs: ShellArgs(command), ToolCallID: "shell-1", Content: out, Status: "✓"}
}

func numbered(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line-" + strconv.Itoa(i+1)
	}
	return strings.Join(lines, "\n")
}

func TestShellPanelTitlesTheCommand(t *testing.T) {
	out, _ := MessageList{Messages: []Message{shellMsg("git status", "clean")}, Width: 80}.Render()
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "shell · $ git status") {
		t.Fatalf("title missing:\n%s", plain)
	}
	if !strings.Contains(plain, "ctrl+c copy") {
		t.Fatalf("copy hint missing:\n%s", plain)
	}
	if !strings.Contains(plain, "clean") {
		t.Fatalf("output missing:\n%s", plain)
	}
}

// TestShellPanelCollapsesToTheTail: collapsed, the panel keeps the last lines
// and a hint whose hidden text is the head; ctrl+o (ExpandAll) shows it all.
func TestShellPanelCollapsesToTheTail(t *testing.T) {
	body := numbered(20)
	msgs := []Message{shellMsg("seq 20", body)}

	out, lm := MessageList{Messages: msgs, Width: 80}.Render()
	plain := ansi.Strip(out)
	if strings.Contains(plain, "line-1\n") || strings.Contains(plain, "line-14 ") {
		t.Fatalf("collapsed panel shows the head:\n%s", plain)
	}
	if !strings.Contains(plain, "line-20") || !strings.Contains(plain, "line-15") {
		t.Fatalf("collapsed panel lost the tail:\n%s", plain)
	}
	if !strings.Contains(plain, "… 14 earlier lines") {
		t.Fatalf("hint missing:\n%s", plain)
	}
	var hidden string
	collapsed, copyable := false, false
	for _, l := range lm {
		if l.MarkerWidth > 0 {
			hidden = l.Hidden
		}
		collapsed = collapsed || l.Collapsed
		copyable = copyable || l.Copyable
	}
	if !strings.HasPrefix(hidden, "line-1\n") || !strings.HasSuffix(hidden, "line-14") {
		t.Fatalf("hint hides %q", hidden)
	}
	if !collapsed || !copyable {
		t.Fatalf("collapsed=%v copyable=%v, want both", collapsed, copyable)
	}

	out, lm = MessageList{Messages: msgs, Width: 80, ExpandAll: true}.Render()
	plain = ansi.Strip(out)
	for i := 1; i <= 20; i++ {
		if !strings.Contains(plain, "line-"+strconv.Itoa(i)) {
			t.Fatalf("expanded panel lost line %d:\n%s", i, plain)
		}
	}
	for _, l := range lm {
		if l.Collapsed {
			t.Fatal("expanded panel still reports collapsed")
		}
	}
}

// TestShellPanelIsNotToolChatter: the panel ignores the ctrl+t gate and never
// folds into the belai panel beside it.
func TestShellPanelIsNotToolChatter(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "a notice"},
		shellMsg("echo hi", "hi-output"),
		{Role: "system", Content: "another notice"},
	}
	out, _ := MessageList{Messages: msgs, Width: 80, ShowTools: false}.Render()
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "hi-output") {
		t.Fatalf("shell panel hidden with tools off:\n%s", plain)
	}
	if n := strings.Count(plain, "╭─ shell"); n != 1 {
		t.Fatalf("shell panel frames = %d, want 1:\n%s", n, plain)
	}
	if !strings.Contains(plain, "a notice") || !strings.Contains(plain, "another notice") {
		t.Fatalf("notices missing:\n%s", plain)
	}
}

// TestShellPanelStripsTerminalControl: the raw bytes stay in Text for copying,
// but nothing that could drive the terminal reaches the screen.
func TestShellPanelStripsTerminalControl(t *testing.T) {
	raw := "before\x1b]52;c;aGk=\x07after \x1b[31mred\x1b[0m\x9b2J"
	msg := shellMsg("printf evil", raw)
	if msg.Text() != raw {
		t.Fatal("Text must keep the raw output")
	}
	out, _ := MessageList{Messages: []Message{msg}, Width: 80}.Render()
	for _, bad := range []string{"]52;", "aGk=", "\x07", "\x9b", "[31m"} {
		if strings.Contains(ansi.Strip(out), bad) {
			t.Fatalf("rendered panel carries %q:\n%q", bad, out)
		}
	}
	if !strings.Contains(ansi.Strip(out), "before") || !strings.Contains(ansi.Strip(out), "red") {
		t.Fatalf("visible text lost:\n%s", ansi.Strip(out))
	}
}

// TestShellPanelRunningIsNotCached: a running panel shows a live tail and
// elapsed time, so it must re-render every frame.
func TestShellPanelRunningIsNotCached(t *testing.T) {
	msgs := []Message{{Role: ShellRole, ToolArgs: ShellArgs("sleep 1"), ToolCallID: "s", StartedAt: time.Now()}}
	msgs[0].AppendProgress("tick-1")
	list := MessageList{Messages: msgs, Width: 80}
	out, _ := list.Render()
	if !strings.Contains(ansi.Strip(out), "tick-1") {
		t.Fatalf("live tail missing:\n%s", ansi.Strip(out))
	}
	if msgs[0].rc.text != "" {
		t.Fatal("running shell panel was cached")
	}
}
