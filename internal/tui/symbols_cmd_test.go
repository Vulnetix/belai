package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func symbolsApp(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg/a.go"), []byte("package pkg\n\ntype Server struct{}\n\nfunc (s *Server) Handle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(Options{Workdir: root}), root
}

// run executes the command's background work and delivers its message, as the
// program loop would.
func runSymbols(t *testing.T, a *App, arg string) string {
	t.Helper()
	cmd := a.symbolsCommand(arg)
	if cmd == nil {
		return a.messages[len(a.messages)-1].Text()
	}
	msg, ok := cmd().(symbolsMsg)
	if !ok {
		t.Fatalf("command returned %T", cmd())
	}
	a.handleSymbols(msg)
	return a.messages[len(a.messages)-1].Text()
}

func TestSymbolsCommandAnswersStatusNameAndPath(t *testing.T) {
	a, _ := symbolsApp(t)
	if got := runSymbols(t, a, ""); !strings.Contains(got, "symbols: 2 declaration(s) in 1 of 1 source file(s)") {
		t.Fatalf("status = %q", got)
	}
	if got := runSymbols(t, a, "Handle"); !strings.Contains(got, "pkg/a.go:5  method Server.Handle") {
		t.Fatalf("name = %q", got)
	}
	if got := runSymbols(t, a, "pkg/a.go"); !strings.Contains(got, "3  struct Server") {
		t.Fatalf("path = %q", got)
	}
	if got := runSymbols(t, a, "refresh"); !strings.Contains(got, "symbols: 2 declaration(s)") {
		t.Fatalf("refresh = %q", got)
	}
	if got := runSymbols(t, a, "not a name!"); !strings.Contains(got, "usage: /symbols") {
		t.Fatalf("bad argument = %q", got)
	}
}

// The rows are for the screen only: ephemeral rows are never recorded, sent to
// a model or synced.
func TestSymbolsRowsAreEphemeral(t *testing.T) {
	a, _ := symbolsApp(t)
	runSymbols(t, a, "Handle")
	last := a.messages[len(a.messages)-1]
	if !last.Ephemeral || last.Role != "system" {
		t.Fatalf("row = %+v", last)
	}
	if !neverPersisted(last) {
		t.Fatal("the row would be written to the session record")
	}
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "Server.Handle") {
			t.Fatalf("a symbols row reached a model turn: %q", turn.Content)
		}
	}
	for _, m := range a.transcriptMessages() {
		if m.Role != "system" && strings.Contains(m.Content, "Server.Handle") {
			t.Fatal("a symbols row became a conversation message")
		}
	}
}

func TestSymbolsCommandHonoursTheSettingAndTheSession(t *testing.T) {
	a, _ := symbolsApp(t)
	off := false
	a.settings.SymbolIndex = &config.SymbolIndexSettings{Enabled: &off}
	if got := runSymbols(t, a, "Handle"); !strings.Contains(got, "symbol index is off") {
		t.Fatalf("off = %q", got)
	}
	a.settings.SymbolIndex = nil
	cmd := a.symbolsCommand("Handle")
	msg := cmd().(symbolsMsg)
	a.sessionID = "another-session"
	n := len(a.messages)
	a.handleSymbols(msg)
	if len(a.messages) != n {
		t.Fatal("an answer for another session was shown")
	}
}

func TestSymbolsAnswerHidesAReadDeniedFile(t *testing.T) {
	a, _ := symbolsApp(t)
	a.settings.Permissions.Deny = []string{"Read(pkg/*)"}
	if got := runSymbols(t, a, "Handle"); strings.Contains(got, "pkg/a.go") {
		t.Fatalf("a denied file was shown: %q", got)
	}
}
