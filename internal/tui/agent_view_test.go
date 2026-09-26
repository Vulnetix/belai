package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func boolPtr(b bool) *bool { return &b }

func writeAgentProfile(t *testing.T, name string, p agentprofile.AgentProfile) {
	t.Helper()
	if p.Name == "" {
		p.Name = name
	}
	if p.Description == "" {
		p.Description = "test agent"
	}
	if p.SystemPrompt == "" {
		p.SystemPrompt = "You are a test agent."
	}
	if p.Mode == "" {
		p.Mode = agentprofile.ModeSingle
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save(%q): %v", name, err)
	}
}

func agentTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	return home
}

func fieldIdx(a *App, key string) int {
	for i, f := range a.agentState.fields {
		if f.key == key {
			return i
		}
	}
	return -1
}

func TestAgentViewListsDiscoveredProfiles(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "alpha", agentprofile.AgentProfile{Description: "first agent"})
	writeAgentProfile(t, "beta", agentprofile.AgentProfile{Description: "second agent"})

	a := New(Options{})
	a.width = 200
	a.push(viewAgent)

	out := a.agentView()
	for _, want := range []string{"alpha", "beta", "first agent", "second agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("agentView missing %q:\n%s", want, out)
		}
	}
	dir, _ := agentprofile.Dir()
	for _, name := range []string{"alpha.json", "beta.json"} {
		path := filepath.Join(dir, name)
		if !strings.Contains(out, path) {
			t.Errorf("agentView missing path %q:\n%s", path, out)
		}
	}
}

func TestAgentViewEditDescription(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "edit-bot", agentprofile.AgentProfile{Description: "before"})

	a := New(Options{})
	a.push(viewAgent)
	a.width = 200
	if a.selectedAgentProfile() == nil {
		t.Fatal("no profile selected")
	}

	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}); cmd != nil {
		t.Fatalf("edit key should be synchronous")
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode after pressing e")
	}

	// The description is the third field (name, file name, description).
	a.agentState.fieldSel = fieldIdx(a, "description")
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.agentState.fieldEdit {
		t.Fatal("expected fieldEdit after pressing enter on description")
	}
	a.editor.SetValue("after")
	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("commit should be synchronous")
	}
	if a.agentState.fieldEdit {
		t.Fatalf("fieldEdit should close after commit")
	}

	reloaded, err := agentprofile.Load("edit-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Description != "after" {
		t.Fatalf("description = %q, want after", reloaded.Description)
	}
}

func TestAgentViewCycleMode(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "mode-bot", agentprofile.AgentProfile{Mode: agentprofile.ModeSingle})

	a := New(Options{})
	a.width = 200
	a.push(viewAgent)
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	idx := fieldIdx(a, "mode")
	if idx < 0 {
		t.Fatal("mode field not found")
	}
	a.agentState.fieldSel = idx

	for _, want := range []string{agentprofile.ModeLoop, agentprofile.ModeSingle, agentprofile.ModeLoop} {
		a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
		got := a.agentState.fields[idx].value
		if got != want {
			t.Fatalf("mode after space = %q, want %q", got, want)
		}
	}
}

// When a schedule or monitor condition is present, mode is derived and the
// toggle is disabled so it can no longer get stuck on scheduled/monitor.
func TestAgentViewModeDisabledWhenDerived(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "derived-bot", agentprofile.AgentProfile{
		Mode:     agentprofile.ModeSingle,
		Schedule: "0 * * * *",
	})

	a := New(Options{})
	a.width = 200
	a.push(viewAgent)
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	idx := fieldIdx(a, "mode")
	a.agentState.fieldSel = idx
	if a.agentState.fields[idx].value != agentprofile.ModeScheduled {
		t.Fatalf("expected derived mode scheduled, got %q", a.agentState.fields[idx].value)
	}

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
	if a.agentState.fields[idx].value != agentprofile.ModeScheduled {
		t.Fatalf("mode should stay scheduled when derived, got %q", a.agentState.fields[idx].value)
	}
	if !strings.Contains(a.agentState.errorMsg, "mode is set by schedule") {
		t.Fatalf("expected disabled-mode error, got %q", a.agentState.errorMsg)
	}
}

func TestAgentBuilderDoneOpensEditForNewProfile(t *testing.T) {
	agentTestHome(t)
	p := agentprofile.AgentProfile{
		Name:         "new-bot",
		Description:  "created",
		SystemPrompt: "sp",
		Mode:         agentprofile.ModeSingle,
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	a := New(Options{})
	a.handleAgentBuilderDone(agentBuilderDoneMsg{name: "new-bot", profile: p, path: "/tmp/new-bot.json"})

	if a.view != viewAgent {
		t.Fatalf("view = %v, want viewAgent", a.view)
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode for newly created agent")
	}
	if p := a.selectedAgentProfile(); p == nil || p.Name != "new-bot" {
		t.Fatalf("selected profile = %v, want new-bot", p)
	}
}

func TestAgentEditCommandMissingName(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.handleCommand("/agent edit")
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "agent edit <name>") {
		t.Fatalf("expected usage message, got %q", last.Text())
	}
}

func TestAgentEditCommandOpensView(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "open-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.handleCommand("/agent edit open-bot")

	if a.view != viewAgent {
		t.Fatalf("view = %v, want viewAgent", a.view)
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode")
	}
}

func TestAgentFieldsIncludeProviderModelEffortGates(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "fields-bot", agentprofile.AgentProfile{
		Provider:      "openai",
		Model:         "gpt-5",
		Effort:        "low",
		Guardrails:    boolPtr(false),
		AskPermission: boolPtr(false),
	})

	a := New(Options{})
	a.push(viewAgent)
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	keys := map[string]bool{}
	for _, f := range a.agentState.fields {
		keys[f.key] = true
	}
	for _, want := range []string{"provider", "model", "effort", "guardrails", "ask_permission"} {
		if !keys[want] {
			t.Fatalf("missing field %q in edit rows", want)
		}
	}

	idx := fieldIdx(a, "guardrails")
	if idx < 0 {
		t.Fatal("guardrails field not found")
	}
	a.agentState.fieldSel = idx
	cycles := []struct {
		wantValue string
		wantBool  *bool
	}{
		{"inherit", nil},
		{"on", boolPtr(true)},
		{"off", boolPtr(false)},
	}
	for _, c := range cycles {
		a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
		p := a.selectedAgentProfile()
		if p == nil {
			t.Fatal("profile disappeared after space")
		}
		if a.agentState.fields[idx].value != c.wantValue {
			t.Fatalf("guardrails value = %q, want %q", a.agentState.fields[idx].value, c.wantValue)
		}
		if (p.Guardrails == nil) != (c.wantBool == nil) || (p.Guardrails != nil && *p.Guardrails != *c.wantBool) {
			t.Fatalf("guardrails pointer mismatch after value %q", c.wantValue)
		}
	}
}

// Regression: a pending edit profile used to open editMode without building
// fields, so the first enter/space hit fields[0] and panicked.
func TestAgentEditViewDoesNotPanicOnEnter(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "crash-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.width, a.height = 120, 40
	a.agentState.pendingEditProfile = "crash-bot"
	a.push(viewAgent)

	if !a.agentState.editMode {
		t.Fatal("expected editMode")
	}
	if len(a.agentState.fields) == 0 {
		t.Fatal("expected fields to be built")
	}
	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter should be synchronous")
	}
	if !a.agentState.fieldEdit {
		t.Fatal("expected the field editor to open")
	}
}

func TestAgentEditDispatchDoesNotPanicOnEnter(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "crash-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.width, a.height = 120, 40
	a.handleCommand("/agent edit crash-bot")

	if !a.agentState.editMode {
		t.Fatal("expected editMode")
	}
	if len(a.agentState.fields) == 0 {
		t.Fatal("expected fields to be built")
	}
	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter should be synchronous")
	}
	if !a.agentState.fieldEdit {
		t.Fatal("expected the field editor to open")
	}
}

func TestAgentFieldsCoverEveryProfileField(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "every-bot", agentprofile.AgentProfile{
		Provider:      "openai",
		Model:         "gpt-5",
		Effort:        "low",
		Guardrails:    boolPtr(true),
		AskPermission: boolPtr(false),
	})

	a := New(Options{})
	a.push(viewAgent)
	a.enterAgentEdit(0)

	want := []string{
		"name", "file_name", "description", "system_prompt", "tools", "mode",
		"schedule", "monitor_condition", "provider", "model", "effort",
		"autonomy", "guardrails", "ask_permission", "reflection", "max_iterations",
	}
	got := make([]string, 0, len(a.agentState.fields))
	for _, f := range a.agentState.fields {
		got = append(got, f.key)
	}
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if !reflect.DeepEqual(got, sortedWant) {
		t.Fatalf("field keys = %v, want %v", got, sortedWant)
	}
}

func TestEveryAgentFieldPersists(t *testing.T) {
	base := agentprofile.AgentProfile{
		Name:          "every-bot",
		Description:   "d",
		SystemPrompt:  "sp",
		Tools:         []string{"Read"},
		Mode:          agentprofile.ModeSingle,
		Provider:      "openai",
		Model:         "gpt-5",
		Effort:        "low",
		Autonomy:      agentprofile.AutonomySupervised,
		Guardrails:    boolPtr(true),
		AskPermission: boolPtr(true),
		Reflection:    false,
		MaxIterations: 3,
	}
	cases := []struct {
		key  string
		raw  string
		want func(t *testing.T, p agentprofile.AgentProfile)
	}{
		{"file_name", "custom-file.json", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.File != "custom-file.json" {
				t.Fatalf("File = %q, want custom-file.json", p.File)
			}
		}},
		{"description", "described", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Description != "described" {
				t.Fatalf("Description = %q", p.Description)
			}
		}},
		{"system_prompt", "system\nprompt", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.SystemPrompt != "system\nprompt" {
				t.Fatalf("SystemPrompt = %q", p.SystemPrompt)
			}
		}},
		{"tools", "Glob\nGrep", func(t *testing.T, p agentprofile.AgentProfile) {
			if !reflect.DeepEqual(p.Tools, []string{"Glob", "Grep"}) {
				t.Fatalf("Tools = %v", p.Tools)
			}
		}},
		{"mode", agentprofile.ModeLoop, func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Mode != agentprofile.ModeLoop {
				t.Fatalf("Mode = %q", p.Mode)
			}
		}},
		{"schedule", "0 * * * *", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Schedule != "0 * * * *" {
				t.Fatalf("Schedule = %q", p.Schedule)
			}
			if p.Mode != agentprofile.ModeScheduled {
				t.Fatalf("Mode = %q, want scheduled when schedule is set", p.Mode)
			}
		}},
		{"monitor_condition", "changes", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.MonitorCondition != "changes" {
				t.Fatalf("MonitorCondition = %q", p.MonitorCondition)
			}
			if p.Mode != agentprofile.ModeMonitor {
				t.Fatalf("Mode = %q, want monitor when monitor_condition is set", p.Mode)
			}
		}},
		{"provider", "", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Provider != "" {
				t.Fatalf("Provider = %q, want inherit", p.Provider)
			}
		}},
		{"model", "", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Model != "" {
				t.Fatalf("Model = %q, want inherit", p.Model)
			}
		}},
		{"effort", "none", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Effort != "none" {
				t.Fatalf("Effort = %q", p.Effort)
			}
		}},
		{"autonomy", agentprofile.AutonomyAutonomous, func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Autonomy != agentprofile.AutonomyAutonomous {
				t.Fatalf("Autonomy = %q", p.Autonomy)
			}
		}},
		{"guardrails", "on", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.Guardrails == nil || !*p.Guardrails {
				t.Fatal("Guardrails not on")
			}
		}},
		{"ask_permission", "off", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.AskPermission == nil || *p.AskPermission {
				t.Fatal("AskPermission not off")
			}
		}},
		{"reflection", "on", func(t *testing.T, p agentprofile.AgentProfile) {
			if !p.Reflection {
				t.Fatal("Reflection not on")
			}
		}},
		{"max_iterations", "7", func(t *testing.T, p agentprofile.AgentProfile) {
			if p.MaxIterations != 7 {
				t.Fatalf("MaxIterations = %d", p.MaxIterations)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			agentTestHome(t)
			if _, err := agentprofile.Save(base); err != nil {
				t.Fatalf("Save: %v", err)
			}
			a := New(Options{})
			a.push(viewAgent)
			a.enterAgentEdit(0)
			idx := fieldIdx(a, tc.key)
			if idx < 0 {
				t.Fatalf("field %q not found", tc.key)
			}
			if err := a.applyAgentFieldChange(a.agentState.fields[idx], tc.raw); err != nil {
				t.Fatalf("apply %s: %v", tc.key, err)
			}
			loaded, err := agentprofile.Load("every-bot")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.want(t, loaded)
		})
	}
}

func TestAgentRenameByNameMovesFile(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "rename-bot", agentprofile.AgentProfile{})

	dir, _ := agentprofile.Dir()
	old := filepath.Join(dir, "rename-bot.json")

	a := New(Options{})
	a.push(viewAgent)
	a.enterAgentEdit(0)
	idx := fieldIdx(a, "name")
	if idx < 0 {
		t.Fatal("name field not found")
	}
	if err := a.applyAgentFieldChange(a.agentState.fields[idx], "renamed-bot"); err != nil {
		t.Fatalf("apply name: %v", err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still exists after rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "renamed-bot.json")); err != nil {
		t.Fatalf("new file missing: %v", err)
	}
	loaded, err := agentprofile.Load("renamed-bot")
	if err != nil {
		t.Fatalf("Load renamed: %v", err)
	}
	if loaded.Name != "renamed-bot" {
		t.Fatalf("Name = %q", loaded.Name)
	}
}

func TestAgentRenameByFileNameMovesFile(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "file-bot", agentprofile.AgentProfile{})

	dir, _ := agentprofile.Dir()
	old := filepath.Join(dir, "file-bot.json")

	a := New(Options{})
	a.push(viewAgent)
	a.enterAgentEdit(0)
	idx := fieldIdx(a, "file_name")
	if idx < 0 {
		t.Fatal("file_name field not found")
	}
	if err := a.applyAgentFieldChange(a.agentState.fields[idx], "custom.json"); err != nil {
		t.Fatalf("apply file_name: %v", err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still exists after rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "custom.json")); err != nil {
		t.Fatalf("new file missing: %v", err)
	}
	loaded, err := agentprofile.Load("file-bot")
	if err != nil {
		t.Fatalf("Load by name after file rename: %v", err)
	}
	if loaded.Name != "file-bot" || loaded.File != "custom.json" {
		t.Fatalf("loaded = %+v, want name file-bot and file custom.json", loaded)
	}
}

func TestAgentToolsPickerCommitsSortedSelection(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "tools-bot", agentprofile.AgentProfile{Tools: []string{"Read"}})

	a := New(Options{})
	a.push(viewAgent)
	a.enterAgentEdit(0)
	idx := fieldIdx(a, "tools")
	if idx < 0 {
		t.Fatal("tools field not found")
	}
	a.agentState.fieldSel = idx
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.agentState.toolEdit {
		t.Fatal("expected tools picker")
	}

	opts := agentprofile.KnownTools()
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	grepIdx := indexOfString(opts, "Grep")
	globIdx := indexOfString(opts, "Glob")
	if grepIdx < 0 || globIdx < 0 {
		t.Fatalf("Grep/Glob missing from KnownTools: %v", opts)
	}
	a.agentState.toolSel = grepIdx
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
	a.agentState.toolSel = globIdx
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter})

	loaded, err := agentprofile.Load("tools-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Tools, []string{"Glob", "Grep"}) {
		t.Fatalf("Tools = %v, want sorted [Glob Grep]", loaded.Tools)
	}
}

func TestAgentBuiltinIsReadOnlyAndDuplicate(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.push(viewAgent)

	idx := -1
	for i, p := range a.agentState.profiles {
		if p.Builtin {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no built-in profile discovered")
	}
	builtinName := a.agentState.profiles[idx].Name
	a.enterAgentEdit(idx)

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
	if a.agentState.errorMsg != "built-in profile is read-only — d duplicates it" {
		t.Fatalf("errorMsg = %q", a.agentState.errorMsg)
	}

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !a.agentState.editMode {
		t.Fatal("expected editor to open on the duplicate")
	}
	p := a.selectedAgentProfile()
	if p == nil || p.Builtin || agentprofile.IsBuiltin(p.Name) {
		t.Fatalf("duplicate is still built-in: %+v", p)
	}
	if strings.HasPrefix(p.Name, agentprofile.BuiltinPrefix) {
		t.Fatalf("duplicate kept builtin prefix: %q", p.Name)
	}
	loaded, err := agentprofile.Load(p.Name)
	if err != nil {
		t.Fatalf("Load duplicate: %v", err)
	}
	if loaded.Builtin || loaded.Name != p.Name {
		t.Fatalf("duplicate load = %+v", loaded)
	}
	_ = builtinName
}

func TestAgentDeleteProfileInEditor(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "delete-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.width, a.height = 200, 40
	a.push(viewAgent)
	a.enterAgentEdit(0)

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if !a.agentState.confirmDelete {
		t.Fatal("x should initiate delete confirmation")
	}

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if a.agentState.confirmDelete {
		t.Fatal("n should cancel delete confirmation")
	}
	if !a.agentState.editMode {
		t.Fatal("editor should stay open after cancel")
	}

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if a.agentState.editMode {
		t.Fatal("editor should close after delete")
	}
	if _, err := agentprofile.Load("delete-bot"); err == nil {
		t.Fatal("delete-bot should have been deleted")
	}
}

func TestAgentDeleteBuiltinRejectedInEditor(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.push(viewAgent)

	idx := -1
	for i, p := range a.agentState.profiles {
		if p.Builtin {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no built-in profile discovered")
	}
	a.enterAgentEdit(idx)
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if a.agentState.confirmDelete {
		t.Fatal("x must not initiate delete for built-ins")
	}
	if !strings.Contains(a.agentState.errorMsg, "read-only") {
		t.Fatalf("expected read-only error, got %q", a.agentState.errorMsg)
	}
}

func TestNextChoiceNilOpts(t *testing.T) {
	if got := nextChoice(nil, "x"); got != "x" {
		t.Fatalf("nextChoice(nil, x) = %q, want x", got)
	}
	if got := prevChoice(nil, "x"); got != "x" {
		t.Fatalf("prevChoice(nil, x) = %q, want x", got)
	}
}

func TestAgentFieldScrollKeepsSelectionVisible(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "scroll-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.width, a.height = 80, 20
	a.push(viewAgent)
	a.enterAgentEdit(0)

	a.agentState.fieldSel = len(a.agentState.fields) - 1
	a.clampAgentFieldScroll()
	if a.agentState.fieldScroll == 0 {
		t.Fatal("expected the window to scroll down")
	}
	window := a.agentEditWindowHeight()
	if a.agentState.fieldSel < a.agentState.fieldScroll || a.agentState.fieldSel >= a.agentState.fieldScroll+window {
		t.Fatalf("selected %d outside window [%d, %d)", a.agentState.fieldSel, a.agentState.fieldScroll, a.agentState.fieldScroll+window)
	}

	out := a.agentEditView(a.contentWidth())
	sel := a.agentState.fields[a.agentState.fieldSel]
	if !strings.Contains(out, sel.label) {
		t.Fatalf("rendered editor missing selected field %q:\n%s", sel.label, out)
	}
}

func TestAgentEditGolden(t *testing.T) {
	path := filepath.Join("testdata", "agent_edit.golden")

	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(old)

	// A fixed BELAI_HOME keeps the on-disk path in the header deterministic.
	t.Setenv("BELAI_HOME", "/belai-golden")
	p := agentprofile.AgentProfile{
		Name:             "golden-bot",
		Description:      "Golden agent",
		SystemPrompt:     "Line one\nLine two\nLine three",
		Tools:            []string{"Read", "Grep", "Glob"},
		Mode:             agentprofile.ModeLoop,
		Schedule:         "0 * * * *",
		MonitorCondition: "changes",
		Provider:         "openai",
		Model:            "gpt-5",
		Effort:           "low",
		Autonomy:         agentprofile.AutonomySupervised,
		Guardrails:       boolPtr(true),
		AskPermission:    boolPtr(false),
		Reflection:       true,
		MaxIterations:    5,
	}

	a := New(Options{})
	a.width, a.height = 80, 40
	a.agentState.profiles = []agentprofile.AgentProfile{p}
	a.enterAgentEdit(0)
	got := a.agentView()

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("line %d:\n  got  %q\n  want %q", i, g, w)
			}
		}
	}
}
