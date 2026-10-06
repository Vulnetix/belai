package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// fakeMCP is a server tool: KindMCP, mutating, named like the real ones.
type fakeMCP struct{ calls *int }

func (fakeMCP) Definition() tools.Definition {
	return tools.Definition{
		Name:        "mcp__gh__get_issue",
		Description: "Get an issue.",
		Properties:  map[string]tools.Property{"number": {Type: "string"}},
		Required:    []string{"number"},
	}
}
func (fakeMCP) Kind() tools.Kind              { return tools.KindMCP }
func (fakeMCP) Subject(map[string]any) string { return "" }
func (f fakeMCP) Execute(_ context.Context, a map[string]any) (tools.Result, error) {
	*f.calls++
	return tools.Result{Kind: tools.KindMCP, Content: "issue " + a["number"].(string)}, nil
}

func newCodeSession(t *testing.T, root, url string, settings config.Settings) (*Session, *int) {
	t.Helper()
	n := new(int)
	cfg := run.Config{Provider: "openai", BaseURL: url, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Registry:      tools.Default(root, false).With(fakeMCP{calls: n}),
		Posture:       posture.AllIgnore(),
		Workdir:       root,
		Settings:      settings,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess, n
}

func codeAdvertised(openAI []string, name string) bool {
	for _, n := range openAI {
		if n == name {
			return true
		}
	}
	return false
}

func surfaceNames(s *Session) []string {
	_, openAI, _ := s.toolSurface()
	var out []string
	for _, d := range openAI {
		out = append(out, d.Function.Name)
	}
	return out
}

// TestCodeModeSurfaceLeavesOtherModesAlone pins the isolation rule: Code is
// advertised and resolvable only on a code turn, MCP only outside it.
func TestCodeModeSurfaceLeavesOtherModesAlone(t *testing.T) {
	sess, _ := newCodeSession(t, t.TempDir(), "http://127.0.0.1:0", config.Settings{})

	// An agent turn: MCP is on the surface (deferred or not), Code is nowhere.
	reg, _, _ := sess.surfaceFull()
	if _, ok := reg.Find("Code"); ok {
		t.Fatal("agent mode must not offer Code")
	}
	if _, ok := reg.Find("mcp__gh__get_issue"); !ok {
		t.Fatal("agent mode must keep MCP tools")
	}
	if tool, _ := sess.execTool("Code"); tool != nil {
		t.Fatal("agent mode must not resolve Code")
	}
	if tool, _ := sess.execTool("mcp__gh__get_issue"); tool == nil {
		t.Fatal("agent mode must resolve MCP tools")
	}

	// Plan mode: neither.
	sess.planMode = true
	reg, _, _ = sess.surfaceFull()
	if _, ok := reg.Find("Code"); ok {
		t.Fatal("plan mode must not offer Code")
	}
	if _, ok := reg.Find("mcp__gh__get_issue"); ok {
		t.Fatal("plan mode must not offer MCP")
	}
	sess.planMode = false

	// A code turn.
	sess.turnCode = true
	reg, _, _ = sess.surfaceFull()
	if _, ok := reg.Find("Code"); !ok {
		t.Fatal("code mode must offer Code")
	}
	if _, ok := reg.Find("mcp__gh__get_issue"); ok {
		t.Fatal("code mode must not advertise MCP tools")
	}
	if !codeAdvertised(surfaceNames(sess), "Code") {
		t.Fatalf("Code is a core tool and must be advertised in full: %v", surfaceNames(sess))
	}
	tool, refusal := sess.execTool("mcp__gh__get_issue")
	if tool != nil || !strings.Contains(refusal, "mcp.<server>.<tool>") {
		t.Fatalf("a direct MCP call in code mode must be refused with the pointer, got %v %q", tool, refusal)
	}
	if tool, _ := sess.execTool("Code"); tool == nil {
		t.Fatal("code mode must resolve Code")
	}
}

func TestCodeModeDisabledByEnabledSetting(t *testing.T) {
	off := false
	sess, _ := newCodeSession(t, t.TempDir(), "http://127.0.0.1:0", config.Settings{Code: &config.CodeSettings{Enabled: &off}})
	if sess.codeRegistry != nil {
		t.Fatal("code.enabled=false must build no code surface")
	}
}

// TestCodeTurnRunsScriptThroughTheGate drives a real code-mode turn: the model
// emits one Code call whose script reads a file, calls an MCP tool and writes
// a file. All three run, the nested calls are visible as events, and the MCP
// tool is reachable only that way.
func TestCodeTurnRunsScriptThroughTheGate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "in.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `
var r = tools.Read({file_path: "in.txt"});
var issue = mcp.gh.get_issue({number: "7"});
tools.Write({file_path: "out.txt", content: "wrote " + issue});
print(r.indexOf("beta") >= 0, issue);`
	args, _ := json.Marshal(map[string]string{"code": script})
	srv := mockSecurityServer("Code", string(args), "done")
	defer srv.Close()
	sess, mcpCalls := newCodeSession(t, root, srv.URL, config.Settings{})
	sess.client = srv.Client()

	var codeResult string
	nested := map[string]bool{}
	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "summarise", ForceMode: modes.ModeCode}, false, func(e Event) {
		switch e.Kind {
		case EventToolStartKind:
			if e.Tool != nil && strings.Contains(e.Tool.ID, ".") {
				nested[e.Tool.Name] = true
			}
		case EventToolResultKind:
			if e.ToolName == "Code" {
				codeResult = e.ToolResult
			}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(codeResult, "true issue 7") {
		t.Fatalf("Code result = %q", codeResult)
	}
	if *mcpCalls != 1 {
		t.Fatalf("MCP tool called %d times, want 1", *mcpCalls)
	}
	got, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil || !strings.HasPrefix(string(got), "wrote issue 7") {
		t.Fatalf("out.txt = %q, %v", got, err)
	}
	for _, n := range []string{"Read", "mcp__gh__get_issue", "Write"} {
		if !nested[n] {
			t.Errorf("nested call %s was not reported; saw %v", n, nested)
		}
	}
	if sess.turnCode {
		t.Fatal("the code latch must be restored after the turn")
	}
}

// TestNestedCallsAreGatedLikeDirectOnes: a deny rule withholds a nested call,
// and a script cannot reach tools outside the nested kinds.
func TestNestedCallsAreGatedLikeDirectOnes(t *testing.T) {
	root := t.TempDir()
	sess, _ := newCodeSession(t, root, "http://127.0.0.1:0", config.Settings{})
	sess.turnCode = true
	ctx := tools.WithNested(context.Background())

	for _, name := range []string{"Code", "Task", "AskUserQuestion", "update_plan", "Todo", "Skill", "ToolSearch"} {
		if tool, refusal := sess.resolveCall(ctx, name); tool != nil {
			t.Errorf("a script must not resolve %s (refusal %q)", name, refusal)
		}
	}
	for _, name := range []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash", "mcp__gh__get_issue"} {
		if tool, refusal := sess.resolveCall(ctx, name); tool == nil {
			t.Errorf("a script should resolve %s: %q", name, refusal)
		}
	}
	sess.turnCode = false
	if tool, _ := sess.resolveCall(ctx, "Read"); tool != nil {
		t.Error("nested calls must not resolve outside a code turn")
	}
}

// TestNestedDenyRuleWithholds: a Deny rule written for a tool applies to the
// script's call exactly as to a direct one, and the script sees the withheld
// message, not the file.
func TestNestedDenyRuleWithholds(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"code": `print(tools.Read({file_path: "secret.txt"}))`})
	srv := mockSecurityServer("Code", string(args), "done")
	defer srv.Close()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Registry:      tools.Default(root, false),
		Perms:         permissions.Settings{Deny: []string{"Read(secret.txt)"}},
		Posture:       posture.AllIgnore(),
		Workdir:       root,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.client = srv.Client()
	var codeResult string
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "go", ForceMode: modes.ModeCode}, false, func(e Event) {
		if e.Kind == EventToolResultKind && e.ToolName == "Code" {
			codeResult = e.ToolResult
		}
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codeResult, "topsecret") || !strings.Contains(codeResult, "permission denied") {
		t.Fatalf("Code result = %q", codeResult)
	}
}
