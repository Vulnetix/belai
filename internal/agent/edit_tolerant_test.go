package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func editSession(t *testing.T, root string, tolerant *bool) *Session {
	t.Helper()
	s, err := NewSession(Options{
		Cfg:         run.Config{Provider: "openai", BaseURL: "http://localhost", APIKey: "test", Model: "test"},
		Registry:    tools.Default(root, false),
		Posture:     posture.AllIgnore(),
		Workdir:     root,
		AskDisabled: true,
		Perms:       permissions.Settings{Allow: []string{"Edit", "Read"}},
		Settings:    config.Settings{Edit: &config.EditSettings{Tolerant: tolerant}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func editTool(t *testing.T, s *Session) *tools.Edit {
	t.Helper()
	tool, ok := s.registry.Find("Edit")
	if !ok {
		t.Fatal("no Edit tool")
	}
	return tool.(*tools.Edit)
}

func TestSessionSetsEditToleranceFromSettings(t *testing.T) {
	root := t.TempDir()
	on, off := true, false
	if !editTool(t, editSession(t, root, nil)).Tolerant {
		t.Fatal("edit.tolerant must default to on")
	}
	if !editTool(t, editSession(t, root, &on)).Tolerant {
		t.Fatal("edit.tolerant true not applied")
	}
	if editTool(t, editSession(t, root, &off)).Tolerant {
		t.Fatal("edit.tolerant false not applied")
	}
}

// A relaxed edit runs through the same pipeline as an exact one and writes the
// fitted bytes; a withheld Read of the file keeps its text out of the
// diagnostic.
func TestEditThroughTheSessionAndWithheldDiagnostic(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	if err := os.WriteFile(path, []byte("a  \r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := editSession(t, root, nil)
	read := s.executeCall(context.Background(), rolemanager.ToolCall{Name: "Read", Args: map[string]any{"file_path": "f.go"}}, func(Event) {}, nil)
	if strings.Contains(read, "withheld") {
		t.Fatalf("read: %q", read)
	}
	out := s.executeCall(context.Background(), rolemanager.ToolCall{Name: "Edit", Args: map[string]any{"file_path": "f.go", "old_string": "a\nb\n", "new_string": "x\ny\n"}}, func(Event) {}, nil)
	if !strings.Contains(out, "matched with trailing whitespace ignored") && !strings.Contains(out, "matched with line endings folded") {
		t.Fatalf("edit result = %q", out)
	}
	if got, _ := os.ReadFile(path); string(got) != "x\r\ny\r\n" {
		t.Fatalf("written %q", got)
	}

	secret := filepath.Join(root, "s.txt")
	if err := os.WriteFile(secret, []byte("hunter2_value := 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read = s.executeCall(context.Background(), rolemanager.ToolCall{Name: "Read", Args: map[string]any{"file_path": "s.txt"}}, func(Event) {}, nil)
	if strings.Contains(read, "withheld") {
		t.Fatalf("read: %q", read)
	}
	real, _ := filepath.EvalSymlinks(secret)
	s.flagged.flag(tools.Result{Kind: tools.KindRead, Meta: map[string]any{"abs": real}}, rolemanager.SentinelPromptInjection)
	out = s.executeCall(context.Background(), rolemanager.ToolCall{Name: "Edit", Args: map[string]any{"file_path": "s.txt", "old_string": "hunter2_value := 2\n", "new_string": "z"}}, func(Event) {}, nil)
	if strings.Contains(out, "hunter2_value") {
		t.Fatalf("diagnostic echoed a withheld file: %q", out)
	}
	if !strings.Contains(out, "line 1") {
		t.Fatalf("diagnostic lost the line number: %q", out)
	}
}
