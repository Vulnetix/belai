package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/patchfmt"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func patchSession(t *testing.T, root string, set config.Settings, perms permissions.Settings) *Session {
	t.Helper()
	s, err := NewSession(Options{
		Cfg:         run.Config{Provider: "openai", BaseURL: "http://localhost", APIKey: "test", Model: "test"},
		Registry:    tools.Default(root, false),
		Posture:     posture.AllIgnore(),
		Workdir:     root,
		AskDisabled: true,
		Perms:       perms,
		Settings:    set,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func patchTool(t *testing.T, s *Session) *tools.ApplyPatch {
	t.Helper()
	tool, ok := s.registry.Find(tools.ApplyPatchName)
	if !ok {
		t.Fatal("no apply_patch tool")
	}
	return tool.(*tools.ApplyPatch)
}

func TestSessionSetsPatchToleranceFromSettings(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		word string
		want patchfmt.Tolerance
	}{
		{"", patchfmt.Whitespace},
		{config.PatchStrict, patchfmt.Strict},
		{config.PatchWhitespace, patchfmt.Whitespace},
		{config.PatchUnicode, patchfmt.Unicode},
		{"loose", patchfmt.Strict},
	} {
		s := patchSession(t, root, config.Settings{PatchTolerance: c.word}, permissions.Settings{})
		if got := patchTool(t, s).Tolerance; got != c.want {
			t.Errorf("patch_tolerance %q gave %v, want %v", c.word, got, c.want)
		}
	}
}

func TestPatchToolOffRemovesItFromEverySurface(t *testing.T) {
	root := t.TempDir()
	off := false
	s := patchSession(t, root, config.Settings{PatchTool: &off}, permissions.Settings{})
	if _, ok := s.registry.Find(tools.ApplyPatchName); ok {
		t.Fatal("patch_tool false left apply_patch registered")
	}
	on := patchSession(t, root, config.Settings{}, permissions.Settings{})
	if _, ok := on.registry.Find(tools.ApplyPatchName); !ok {
		t.Fatal("apply_patch is not on by default")
	}
}

func TestPlanModeAndReadOnlyDropApplyPatch(t *testing.T) {
	reg := tools.Default(t.TempDir(), false)
	if _, ok := reg.Plan().Find(tools.ApplyPatchName); ok {
		t.Fatal("the plan surface offers apply_patch")
	}
	if modes.ToolAllowed(tools.ApplyPatchName, nil, true, tools.PlanSurface{}) {
		t.Fatal("plan mode allows apply_patch")
	}
	if !modes.ToolAllowed(tools.ApplyPatchName, nil, true, tools.PlanSurface{GuardrailsOff: true}) {
		t.Fatal("guardrails off should restore the full surface")
	}
}

func TestIsEditToolIncludesApplyPatch(t *testing.T) {
	for _, n := range []string{"Write", "Edit", "apply_patch"} {
		if !isEditTool(n) {
			t.Errorf("%s is not an edit tool for pre_edit/post_edit hooks", n)
		}
	}
	if isEditTool("Bash") {
		t.Error("Bash is an edit tool")
	}
}

func TestApplyPatchPermissionIsJudgedPerFile(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	perms := permissions.Settings{Allow: []string{"Read", "Edit(a.txt)"}, Deny: []string{"Edit(b.txt)"}}
	s := patchSession(t, root, config.Settings{}, perms)
	ctx := context.Background()
	for _, n := range []string{"a.txt", "b.txt"} {
		if out := s.executeCall(ctx, rolemanager.ToolCall{Name: "Read", Args: map[string]any{"file_path": n}}, func(Event) {}, nil); strings.Contains(out, "withheld") {
			t.Fatalf("read %s: %q", n, out)
		}
	}
	both := "*** Begin Patch\n*** Update File: a.txt\n-x\n+A\n*** Update File: b.txt\n-x\n+B\n*** End Patch\n"
	out := s.executeCall(ctx, rolemanager.ToolCall{Name: "apply_patch", Args: map[string]any{"input": both}}, func(Event) {}, nil)
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("a deny on one file did not withhold the call: %q", out)
	}
	for _, n := range []string{"a.txt", "b.txt"} {
		if b, _ := os.ReadFile(filepath.Join(root, n)); string(b) != "x\n" {
			t.Fatalf("%s changed: %q", n, b)
		}
	}
	only := "*** Begin Patch\n*** Update File: a.txt\n-x\n+A\n*** End Patch\n"
	out = s.executeCall(ctx, rolemanager.ToolCall{Name: "apply_patch", Args: map[string]any{"input": only}}, func(Event) {}, nil)
	if !strings.Contains(out, "applied the patch to 1 file") {
		t.Fatalf("an allowed patch did not run: %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "A\n" {
		t.Fatalf("a.txt = %q", b)
	}
}

func TestDecideCallAllowMustCoverEveryFile(t *testing.T) {
	root := t.TempDir()
	s := patchSession(t, root, config.Settings{}, permissions.Settings{Allow: []string{"Edit(src/*)"}})
	tool := patchTool(t, s)
	covered := map[string]any{"input": "*** Begin Patch\n*** Update File: src/a.go\n-a\n+b\n*** End Patch\n"}
	if _, _, matched := s.decideCall("apply_patch", tool, covered); !matched {
		t.Fatal("an Edit rule covering the only file did not match")
	}
	wider := map[string]any{"input": "*** Begin Patch\n*** Update File: src/a.go\n-a\n+b\n*** Update File: go.mod\n-a\n+b\n*** End Patch\n"}
	if _, _, matched := s.decideCall("apply_patch", tool, wider); matched {
		t.Fatal("a rule for src/* approved a patch that also edits go.mod")
	}
}

func TestAskSubjectNamesEveryFile(t *testing.T) {
	root := t.TempDir()
	s := patchSession(t, root, config.Settings{}, permissions.Settings{})
	got := askSubject(patchTool(t, s), map[string]any{"input": "*** Begin Patch\n*** Update File: a.go\n-a\n+b\n*** Add File: b.go\n+x\n*** End Patch\n"})
	if got != "a.go, b.go" {
		t.Fatalf("subject = %q", got)
	}
}
