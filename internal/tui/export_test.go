package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/session"
)

func TestExportCommandWritesMarkdown(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})

	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: "please build"})
	a.appendEntry(session.Entry{Type: "assistant", Role: "assistant", Content: "done"})

	msg := a.exportSessionCmd("")().(exportDoneMsg)
	if msg.err != nil {
		t.Fatalf("export: %v", msg.err)
	}
	wantPath := filepath.Join(config.ProjectExportsDir(workdir), a.sessionID+".md")
	if msg.path != wantPath {
		t.Fatalf("path = %q, want %q", msg.path, wantPath)
	}

	data, err := os.ReadFile(msg.path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("export file is empty")
	}

	fi, err := os.Stat(msg.path)
	if err != nil {
		t.Fatalf("stat export: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode = %o, want 0600", fi.Mode().Perm())
	}
}
