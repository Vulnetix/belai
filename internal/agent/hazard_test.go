package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

const workflowArgs = `{"file_path":".github/workflows/ci.yml","content":"on: push\n"}`

type hazardRun struct {
	asked   *AskRequest
	asks    int
	result  string
	decided bool
}

// runHazard runs one turn whose model calls toolName with args, answering any
// ask with userAllow.
func runHazard(t *testing.T, root, toolName, args string, o Options, userAllow bool) hazardRun {
	t.Helper()
	srv := mockSecurityServer(toolName, args, "done")
	defer srv.Close()
	o.Cfg = run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"}
	o.Client = srv.Client()
	if o.Registry == nil {
		o.Registry = tools.NewRegistry(&tools.Write{Root: root}, &tools.Bash{Root: root})
	}
	if o.Posture == nil {
		o.Posture = posture.Defaults()
	}
	o.Workdir = root
	sess, err := NewSession(o)
	if err != nil {
		t.Fatal(err)
	}
	var out hazardRun
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "do it"}, false, func(e Event) {
		switch e.Kind {
		case EventPermissionAskKind:
			out.asks++
			out.asked = e.Ask
			e.AskReply <- PermissionAskReply{Allow: userAllow}
		case EventAskDecidedKind:
			out.decided = true
		case EventToolResultKind:
			if e.ToolName == toolName {
				out.result = e.ToolResult
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

// An allow rule that names the exact call does not skip the floor.
func TestHazardAsksDespiteAnAllowRule(t *testing.T) {
	root := t.TempDir()
	perms := permissions.From([]string{"Write(.github/workflows/ci.yml)", "Write"}, nil, nil)
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, Perms: perms}, true)
	if r.asks != 1 || r.asked == nil || len(r.asked.Hazard) != 1 || r.asked.Hazard[0] != tools.HazardPathCI {
		t.Fatalf("asks=%d ask=%+v", r.asks, r.asked)
	}
	if !exists(root, ".github/workflows/ci.yml") {
		t.Fatal("an approved call did not run")
	}
}

// A refusal withholds the call and writes nothing.
func TestHazardRefusalWritesNothing(t *testing.T) {
	root := t.TempDir()
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true}, false)
	if r.asks != 1 || !strings.Contains(r.result, "permission denied by user") {
		t.Fatalf("asks=%d result=%q", r.asks, r.result)
	}
	if exists(root, ".github/workflows/ci.yml") {
		t.Fatal("a refused call wrote the file")
	}
}

// An ordinary path with an allow rule runs with no ask: the floor adds nothing
// where there is no hazard.
func TestNoHazardNoExtraAsk(t *testing.T) {
	root := t.TempDir()
	perms := permissions.From([]string{"Write"}, nil, nil)
	r := runHazard(t, root, "Write", `{"file_path":"x.txt","content":"hi"}`, Options{AllowAsk: true, Perms: perms}, false)
	if r.asks != 0 || !exists(root, "x.txt") {
		t.Fatalf("asks=%d", r.asks)
	}
}

// Ask off (yolo) does not skip the floor.
func TestHazardAsksWithAskOff(t *testing.T) {
	root := t.TempDir()
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, AskDisabled: true}, false)
	if r.asks != 1 || exists(root, ".github/workflows/ci.yml") {
		t.Fatalf("asks=%d", r.asks)
	}
}

// hazard_yolo_waive lets ask off skip it, and only with ask off.
func TestHazardWaiveNeedsAskOff(t *testing.T) {
	yes := true
	waive := config.Settings{Permissions: config.PermissionRules{HazardYoloWaive: &yes}}

	root := t.TempDir()
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, AskDisabled: true, Settings: waive}, false)
	if r.asks != 0 || !exists(root, ".github/workflows/ci.yml") {
		t.Fatalf("waived with ask off: asks=%d", r.asks)
	}

	root = t.TempDir()
	r = runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, Settings: waive}, false)
	if r.asks != 1 || exists(root, ".github/workflows/ci.yml") {
		t.Fatalf("waive must not apply while ask is on: asks=%d", r.asks)
	}
}

// hazard_ask false in a user layer turns the floor off.
func TestHazardAskOffSettingTurnsFloorOff(t *testing.T) {
	off := false
	s := config.Settings{Permissions: config.PermissionRules{HazardAsk: &off, Allow: []string{"Write"}}}
	root := t.TempDir()
	perms := permissions.From([]string{"Write"}, nil, nil)
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, Settings: s, Perms: perms}, false)
	if r.asks != 0 || !exists(root, ".github/workflows/ci.yml") {
		t.Fatalf("asks=%d", r.asks)
	}
}

// Nobody to ask: withheld, whatever PermissionAskNoTTY says, and the model is
// told which rail.
func TestHazardWithheldWithoutTTYEvenWhenWarn(t *testing.T) {
	root := t.TempDir()
	pol := posture.Defaults()
	pol[posture.PermissionAskNoTTY] = posture.Ignore
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: false, Posture: pol}, true)
	if r.asks != 0 || !strings.Contains(r.result, "hazard rail") || !strings.Contains(r.result, tools.HazardPathCI) {
		t.Fatalf("asks=%d result=%q", r.asks, r.result)
	}
	if exists(root, ".github/workflows/ci.yml") {
		t.Fatal("a withheld call ran")
	}
	// Ask off with nobody to ask is still withheld unless waived.
	r = runHazard(t, t.TempDir(), "Write", workflowArgs, Options{AllowAsk: false, AskDisabled: true, Posture: pol}, true)
	if !strings.Contains(r.result, "hazard rail") {
		t.Fatalf("result=%q", r.result)
	}
}

// A confident decision model neither allows nor refuses a hazard call.
func TestHazardSkipsTheDecisionModel(t *testing.T) {
	root := t.TempDir()
	d := &fakeDecider{th: 0.9, on: true, judge: 0.99}
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, AskDecider: d}, false)
	if r.asks != 1 || r.decided || len(d.judged) != 0 {
		t.Fatalf("asks=%d decided=%v judged=%v", r.asks, r.decided, d.judged)
	}
	if exists(root, ".github/workflows/ci.yml") {
		t.Fatal("the decision model's yes let a hazard call through")
	}
}

// A deny rule still blocks, with no ask.
func TestHazardDenyRuleBlocksWithoutAsking(t *testing.T) {
	root := t.TempDir()
	perms := permissions.From(nil, nil, []string{"Write(.github/**)"})
	r := runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true, Perms: perms}, true)
	if r.asks != 0 || !strings.Contains(r.result, "permission denied") {
		t.Fatalf("asks=%d result=%q", r.asks, r.result)
	}
}

// Bash hazards: the command is parsed, and a refusal means it never runs.
func TestHazardBashAsksAndIsRefused(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	cmd := "touch " + marker + " && sudo true"
	args, _ := json.Marshal(map[string]any{"command": cmd})
	perms := permissions.From([]string{"Bash"}, nil, nil)
	r := runHazard(t, root, "Bash", string(args), Options{AllowAsk: true, Perms: perms}, false)
	if r.asks != 1 || r.asked == nil || len(r.asked.Hazard) != 1 || r.asked.Hazard[0] != "exec.privilege" {
		t.Fatalf("asks=%d ask=%+v", r.asks, r.asked)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused Bash call ran")
	}
	// An unreadable line asks as hazard.opaque.
	args, _ = json.Marshal(map[string]any{"command": "rm -rf \"$DIR\""})
	r = runHazard(t, root, "Bash", string(args), Options{AllowAsk: true, Perms: perms}, false)
	if r.asks != 1 || len(r.asked.Hazard) != 1 || r.asked.Hazard[0] != "hazard.opaque" {
		t.Fatalf("opaque: asks=%d ask=%+v", r.asks, r.asked)
	}
}

// A user path added in settings is a hazard.
func TestHazardUserProtectedPath(t *testing.T) {
	root := t.TempDir()
	s := config.Settings{Permissions: config.PermissionRules{ProtectedPaths: []string{"vault"}}}
	r := runHazard(t, root, "Write", `{"file_path":"vault/key.txt","content":"x"}`, Options{AllowAsk: true, Settings: s}, false)
	if r.asks != 1 || r.asked.Hazard[0] != tools.HazardPathUser {
		t.Fatalf("asks=%d ask=%+v", r.asks, r.asked)
	}
}

// A call from a script takes the same path as a direct call.
func TestHazardCodeModeNestedCallAsks(t *testing.T) {
	root := t.TempDir()
	script := `tools.Write({file_path: ".github/workflows/ci.yml", content: "on: push\n"});`
	args, _ := json.Marshal(map[string]string{"code": script})
	srv := mockSecurityServer("Code", string(args), "done")
	defer srv.Close()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.Default(root, false),
		Posture:       posture.Defaults(),
		Perms:         permissions.From([]string{"Write"}, nil, nil),
		Workdir:       root,
		AllowAsk:      true,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	asks := 0
	var hazard []string
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "go", ForceMode: modes.ModeCode}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			asks++
			hazard = e.Ask.Hazard
			e.AskReply <- PermissionAskReply{Allow: false}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if asks != 1 || len(hazard) != 1 {
		t.Fatalf("asks=%d hazard=%v", asks, hazard)
	}
	if exists(root, ".github/workflows/ci.yml") {
		t.Fatal("a refused nested call wrote the file")
	}
}

// The audit event holds identifiers and the tool name, never the path.
func TestHazardAuditCarriesIdentifiersOnly(t *testing.T) {
	rec, err := audit.Open(t.TempDir(), "test-host")
	if err != nil {
		t.Fatal(err)
	}
	audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(nil) })

	root := t.TempDir()
	runHazard(t, root, "Write", workflowArgs, Options{AllowAsk: true}, false)
	b, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `"kind":"hazard.asked"`) || !strings.Contains(text, `"kind":"hazard.denied"`) {
		t.Fatalf("audit = %s", text)
	}
	if !strings.Contains(text, "path.ci") || strings.Contains(text, "workflows") || strings.Contains(text, "ci.yml") {
		t.Fatalf("the audit carried the path: %s", text)
	}
}
