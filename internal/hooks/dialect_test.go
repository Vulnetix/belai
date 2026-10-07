package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/posture"
)

// pixHooks is the plugin's hooks.json as shipped: a comment block, three events,
// one program on PATH.
const pixHooks = `{"_comment": ["one command"], "hooks": {
  "PreToolUse": [{"matcher": "Bash|Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}],
  "SessionStart": [{"hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}],
  "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}]}}`

// withProgram puts an executable called name on PATH for the test.
func withProgram(t *testing.T, name, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func bundleDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o700
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParseDefinitionReadsThePixHooks(t *testing.T) {
	withProgram(t, "vulnetix", "exit 0")
	dir := t.TempDir()
	p, err := ParseDefinition("pix", dir, []byte(pixHooks), []string{"vulnetix"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hooks) != 3 {
		t.Fatalf("hooks = %d", len(p.Hooks))
	}
	var names []string
	for _, h := range p.Hooks {
		names = append(names, h.Name+"="+h.Event)
		if h.Dialect != DialectClaude || !h.AllowedProgram || h.Dir != dir || h.TimeoutMS != 30_000 || strings.Join(h.Argv, " ") != "vulnetix agent hook" {
			t.Errorf("hook = %+v", h)
		}
	}
	want := "bundle:pix:SessionStart:01-01=session_start bundle:pix:UserPromptSubmit:01-01=user_prompt_submit bundle:pix:PreToolUse:01-01=pre_tool"
	if strings.Join(names, " ") != want {
		t.Errorf("names = %v", names)
	}
	for _, h := range p.Hooks {
		if h.Event == EventPreTool && h.Matcher != "Bash|Edit|Write|MultiEdit" {
			t.Errorf("matcher = %q", h.Matcher)
		}
	}
}

func TestParseDefinitionRefusesAProgramThatIsNotAllowed(t *testing.T) {
	withProgram(t, "vulnetix", "exit 0")
	_, err := ParseDefinition("pix", t.TempDir(), []byte(pixHooks), nil)
	if err == nil || !strings.Contains(err.Error(), "hooks.allowed_programs") {
		t.Fatalf("err = %v", err)
	}
	// Allowed by name but not on PATH is refused too.
	t.Setenv("PATH", t.TempDir())
	if _, err := ParseDefinition("pix", t.TempDir(), []byte(pixHooks), []string{"vulnetix"}); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseDefinitionAcceptsABundleScriptAndThePluginRoot(t *testing.T) {
	dir := bundleDir(t, map[string]string{"guard.sh": "#!/bin/sh\n", "scripts/check.sh": "#!/bin/sh\n", "rules.txt": "x"})
	def := `{"description": "d", "hooks": {"PostToolUse": [{"matcher": ".*", "hooks": [
	  {"type": "command", "command": "guard.sh --rules rules.txt"},
	  {"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/check.sh 'two words'", "timeout": 5}]}]}}`
	p, err := ParseDefinition("g", dir, []byte(def), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hooks) != 2 || p.Hooks[0].AllowedProgram || p.Hooks[0].Matcher != "" {
		t.Fatalf("hooks = %+v", p.Hooks)
	}
	if got := p.Hooks[1].Argv; len(got) != 2 || got[0] != "./scripts/check.sh" || got[1] != "two words" || p.Hooks[1].TimeoutMS != 5000 {
		t.Errorf("second = %+v", p.Hooks[1])
	}
}

func TestParseDefinitionRefusesWhatCouldEscapeOrInject(t *testing.T) {
	withProgram(t, "vulnetix", "exit 0")
	withProgram(t, "python3", "exit 0")
	dir := bundleDir(t, map[string]string{"guard.sh": "#!/bin/sh\n", "rules.txt": "x"})
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	def := func(cmd string) []byte {
		return []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": ` + jsonString(cmd) + `}]}]}}`)
	}
	allowed := []string{"vulnetix", "python3"}
	for what, cmd := range map[string]string{
		"an absolute program":             "/bin/sh guard.sh",
		"a parent path":                   "../guard.sh",
		"a missing script":                "missing.sh",
		"a script through a symlink":      "out/x.sh",
		"a pipe":                          "guard.sh | sh",
		"a command substitution":          "guard.sh $(id)",
		"a variable":                      "guard.sh $HOME",
		"another variable":                "guard.sh ${CLAUDE_PROJECT_DIR}",
		"a redirect":                      "guard.sh > x",
		"a semicolon":                     "guard.sh; id",
		"a backtick":                      "guard.sh `id`",
		"an unclosed quote":               "guard.sh 'a",
		"a tilde":                         "guard.sh ~/x",
		"an absolute argument":            "vulnetix /etc/passwd",
		"a parent argument":               "vulnetix ../x",
		"an absolute flag value":          "vulnetix --config=/etc/x",
		"an interpreter given code":       "python3 -c print(1)",
		"a program that is not allowed":   "curl http://x",
		"an empty command":                " ",
		"a script that is a directory":    "out",
		"a quoted shell word":             "guard.sh '$(id)'",
		"a glob":                          "guard.sh *",
		"a tab and newline inside a word": "guard.sh a\nb;id",
	} {
		if _, err := ParseDefinition("g", dir, def(cmd), allowed); err == nil {
			t.Errorf("%s: accepted %q", what, cmd)
		}
	}
	// Quoted plain text and a relative file argument are fine.
	if _, err := ParseDefinition("g", dir, def(`vulnetix scan --out 'rules.txt'`), allowed); err != nil {
		t.Errorf("a plain argument refused: %v", err)
	}
}

func TestParseDefinitionIsStrict(t *testing.T) {
	dir := bundleDir(t, map[string]string{"g.sh": "x"})
	for what, def := range map[string]string{
		"not an object":       `[]`,
		"an unknown key":      `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "g.sh"}]}]}, "run": "x"}`,
		"no hooks":            `{"name": "x"}`,
		"empty hooks":         `{"hooks": {}}`,
		"a stray group key":   `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "g.sh"}], "if": "x"}]}}`,
		"a stray handler key": `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "g.sh", "async": true}]}]}}`,
		"a bad timeout":       `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "g.sh", "timeout": 0}]}]}}`,
		"a regex matcher":     `{"hooks": {"PreToolUse": [{"matcher": "Bash(git .*)", "hooks": [{"type": "command", "command": "g.sh"}]}]}}`,
		"a dotted matcher":    `{"hooks": {"PreToolUse": [{"matcher": "a.b", "hooks": [{"type": "command", "command": "g.sh"}]}]}}`,
	} {
		if _, err := ParseDefinition("g", dir, []byte(def), nil); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	// What Belai does not run is skipped with a note, not an error.
	p, err := ParseDefinition("g", dir, []byte(`{"hooks": {"PermissionRequest": [{"hooks": [{"type": "command", "command": "g.sh"}]}], "Stop": [{"hooks": [{"type": "prompt", "command": "x"}, {"type": "command", "command": "g.sh", "timeout": 600}]}]}}`), nil)
	if err != nil || len(p.Hooks) != 1 || len(p.Notes) != 2 || p.Hooks[0].TimeoutMS != MaxTimeoutMS {
		t.Fatalf("parsed = %+v %v", p, err)
	}
}

func TestTranslateMatcher(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "*": "", ".*": "", "Bash": "Bash", "Bash|Edit": "Bash|Edit", "^Bash$": "Bash", "mcp__.*": "mcp__*", "mcp__fs__.*|Read": "mcp__fs__*|Read",
	} {
		got, err := translateMatcher(in, EventPreTool)
		if err != nil || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"Bash(.*)", "a.b", "[ab]", "a+", "(Bash|Edit)", "a|", strings.Repeat("a", 129)} {
		if _, err := translateMatcher(bad, EventPreTool); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLoadBundlesLoadsValidAndSkipsTheRest(t *testing.T) {
	var warned []string
	old := Warn
	Warn = func(m string) { warned = append(warned, m) }
	defer func() { Warn = old }()
	root := t.TempDir()
	mk := func(name, def string, scripts ...string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, BundleDefinitionFile), []byte(def), 0o600)
		for _, s := range scripts {
			os.WriteFile(filepath.Join(dir, s), []byte("#!/bin/sh\n"), 0o700)
		}
	}
	mk("good", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a.sh"}]}]}}`, "a.sh")
	mk("bad-script", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "nope.sh"}]}]}}`)
	mk("bad-program", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "vulnetix x"}]}]}}`)
	mk(".new-tmp", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a.sh"}]}]}}`, "a.sh")
	mk("Upper", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a.sh"}]}]}}`, "a.sh")
	os.MkdirAll(filepath.Join(root, "nodef"), 0o700)
	os.WriteFile(filepath.Join(root, "flat.json"), []byte(`{}`), 0o600)
	if err := os.Symlink(filepath.Join(root, "good"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	hs, err := LoadBundles(root, posture.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].Name != "bundle:good:Stop:01-01" || hs[0].Bundle != "good" || hs[0].Dir != filepath.Join(root, "good") {
		t.Fatalf("hooks = %+v", hs)
	}
	joined := strings.Join(warned, "|")
	for _, want := range []string{"bad-script", "bad-program", "nodef", "hooks.allowed_programs"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning mentions %q: %v", want, warned)
		}
	}
	if strings.Contains(joined, "link") || strings.Contains(joined, ".new-tmp") {
		t.Errorf("a symlink or a temp dir was reported: %v", warned)
	}
	if hs, err := LoadBundles(filepath.Join(root, "missing"), posture.Defaults(), nil); err != nil || hs != nil {
		t.Errorf("a missing directory: %v %v", hs, err)
	}
}

func TestLoadBundlesRefusesASymlinkedDefinition(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "b")
	os.MkdirAll(dir, 0o700)
	outside := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(outside, []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a.sh"}]}]}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "a.sh"), []byte("#!/bin/sh\n"), 0o700)
	if err := os.Symlink(outside, filepath.Join(dir, BundleDefinitionFile)); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	old := Warn
	Warn = func(string) {}
	defer func() { Warn = old }()
	if hs, _ := LoadBundles(root, posture.Defaults(), nil); len(hs) != 0 {
		t.Fatalf("a symlinked definition loaded: %+v", hs)
	}
}

// dispatchBundle runs a one-script bundle for event and returns the outcome.
func dispatchBundle(t *testing.T, event, claudeEvent, script string, in Input) Outcome {
	t.Helper()
	dir := bundleDir(t, map[string]string{"h.sh": "#!/bin/sh\n" + script + "\n"})
	def := `{"hooks": {"` + claudeEvent + `": [{"hooks": [{"type": "command", "command": "h.sh", "timeout": 5}]}]}}`
	p, err := ParseDefinition("t", dir, []byte(def), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Set{Hooks: p.Hooks, Runner: &Runner{Timeout: 5 * time.Second, MaxBytes: 64 * 1024}}
	in.Event = event
	return s.Dispatch(context.Background(), in)
}

func TestBundleHookReceivesTheDialectPayload(t *testing.T) {
	dir := bundleDir(t, map[string]string{"h.sh": "#!/bin/sh\ncat > \"$CLAUDE_PLUGIN_ROOT/in.json\"\necho \"$CLAUDE_PROJECT_DIR\" > \"$CLAUDE_PLUGIN_ROOT/proj\"\n"})
	p, err := ParseDefinition("t", dir, []byte(`{"hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "h.sh"}]}]}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Set{Hooks: p.Hooks, Runner: &Runner{Timeout: 5 * time.Second, MaxBytes: 64 * 1024}}
	s.Dispatch(context.Background(), Input{Event: EventPreTool, SessionID: "s1", Cwd: "/work", ToolName: "Bash", ToolInput: map[string]any{"command": "ls"}, ToolUseID: "call-1"})
	got, err := os.ReadFile(filepath.Join(dir, "in.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hook_event_name":"PreToolUse"`, `"session_id":"s1"`, `"tool_name":"Bash"`, `"command":"ls"`, `"tool_use_id":"call-1"`, `"permission_mode":"default"`, `"cwd":"/work"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("payload lacks %s: %s", want, got)
		}
	}
	if strings.Contains(string(got), `"event"`) {
		t.Errorf("a bundle hook got Belai's own payload: %s", got)
	}
	if proj, _ := os.ReadFile(filepath.Join(dir, "proj")); strings.TrimSpace(string(proj)) != "/work" {
		t.Errorf("CLAUDE_PROJECT_DIR = %q", proj)
	}
}

func TestBundleHookAnswersMapOntoThreeDecisions(t *testing.T) {
	tool := Input{ToolName: "Bash"}
	cases := []struct {
		name, script string
		wantDecision string
		wantReason   string
		fails        bool
	}{
		{"silence", `exit 0`, DecisionNone, "", false},
		{"exit 2 blocks with stderr", `echo "no rm -rf" >&2; exit 2`, DecisionDeny, "no rm -rf", false},
		{"exit 1 on a blocking event denies", `exit 1`, DecisionDeny, "", true},
		{"the specific decision", `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"policy"}}'`, DecisionDeny, "policy", false},
		{"ask", `echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"sure?"}}'`, DecisionAsk, "sure?", false},
		{"allow narrows nothing", `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`, DecisionAllow, "", false},
		{"block", `echo '{"decision":"block","reason":"stop"}'`, DecisionDeny, "stop", false},
		{"approve", `echo '{"decision":"approve"}'`, DecisionAllow, "", false},
		{"continue false", `echo '{"continue":false,"stopReason":"halt"}'`, DecisionDeny, "halt", false},
		{"deny wins over allow", `echo '{"decision":"approve","hookSpecificOutput":{"permissionDecision":"deny"}}'`, DecisionDeny, "", false},
		{"unknown keys are ignored", `echo '{"updatedInput":{"command":"rm -rf /"},"systemMessage":"x","decision":"approve"}'`, DecisionAllow, "", false},
		{"an unknown decision denies", `echo '{"decision":"maybe"}'`, DecisionDeny, "", true},
		{"plain text on a tool event denies", `echo hello`, DecisionDeny, "", true},
		{"malformed json denies", `echo '{"decision":'`, DecisionDeny, "", true},
		{"a timeout denies", `sleep 10`, DecisionDeny, "", true},
	}
	for _, c := range cases {
		script := c.script
		claude := "PreToolUse"
		if c.name == "a timeout denies" {
			script = "sleep 3"
		}
		dir := bundleDir(t, map[string]string{"h.sh": "#!/bin/sh\n" + script + "\n"})
		timeout := 5
		if c.name == "a timeout denies" {
			timeout = 1
		}
		def := `{"hooks": {"` + claude + `": [{"hooks": [{"type": "command", "command": "h.sh", "timeout": ` + string(rune('0'+timeout)) + `}]}]}}`
		p, err := ParseDefinition("t", dir, []byte(def), nil)
		if err != nil {
			t.Fatal(err)
		}
		s := &Set{Hooks: p.Hooks, Runner: &Runner{Timeout: 5 * time.Second, MaxBytes: 64 * 1024}}
		in := tool
		in.Event = EventPreTool
		o := s.Dispatch(context.Background(), in)
		if o.Decision != c.wantDecision {
			t.Errorf("%s: decision %q, want %q (%+v)", c.name, o.Decision, c.wantDecision, o)
		}
		if c.wantReason != "" && (len(o.Reasons) == 0 || !strings.Contains(o.Reasons[0].Text, c.wantReason)) {
			t.Errorf("%s: reasons %+v, want %q", c.name, o.Reasons, c.wantReason)
		}
		if c.fails && len(o.Failures) == 0 {
			t.Errorf("%s: no failure recorded", c.name)
		}
	}
}

func TestBundleHookContextAndNonBlockingEvents(t *testing.T) {
	// Plain text on a prompt event is context, as the dialect defines it.
	o := dispatchBundle(t, EventUserPromptSubmit, "UserPromptSubmit", `echo "prior scan: 3 findings"`, Input{Prompt: "hi"})
	if o.Decision != DecisionNone || len(o.Context) != 1 || o.Context[0].Text != "prior scan: 3 findings" {
		t.Fatalf("prompt context = %+v", o)
	}
	// Structured context rides in hookSpecificOutput.
	o = dispatchBundle(t, EventUserPromptSubmit, "UserPromptSubmit", `echo '{"hookSpecificOutput":{"additionalContext":"from json"}}'`, Input{})
	if len(o.Context) != 1 || o.Context[0].Text != "from json" {
		t.Fatalf("json context = %+v", o)
	}
	// A prompt hook that exits 2 blocks the prompt.
	if o = dispatchBundle(t, EventUserPromptSubmit, "UserPromptSubmit", `echo "no" >&2; exit 2`, Input{}); o.Decision != DecisionDeny {
		t.Fatalf("exit 2 on a prompt = %+v", o)
	}
	// A post_tool hook cannot block: a decision and an exit 2 are failures at most.
	o = dispatchBundle(t, EventPostTool, "PostToolUse", `echo '{"decision":"block","reason":"x"}'`, Input{ToolName: "Bash"})
	if o.Decision != DecisionNone || len(o.Failures) != 0 {
		t.Fatalf("post_tool block = %+v", o)
	}
	if o = dispatchBundle(t, EventPostTool, "PostToolUse", `exit 2`, Input{ToolName: "Bash"}); o.Decision != DecisionNone || len(o.Failures) != 1 {
		t.Fatalf("post_tool exit 2 = %+v", o)
	}
	// Noise on a non-blocking event is ignored.
	if o = dispatchBundle(t, EventStop, "Stop", `echo done`, Input{}); o.Decision != DecisionNone || len(o.Failures) != 0 {
		t.Fatalf("stop noise = %+v", o)
	}
	// The output is clipped like any hook's.
	o = dispatchBundle(t, EventUserPromptSubmit, "UserPromptSubmit", `head -c 5000 /dev/zero | tr '\0' a`, Input{})
	if len(o.Context) != 1 || len(o.Context[0].Text) > MaxContextBytes+4 {
		t.Fatalf("context not clipped: %d", len(o.Context[0].Text))
	}
}

func TestAllowedProgramRunsFromPathAndABundleCannotShadowIt(t *testing.T) {
	withProgram(t, "vulnetix", `cat >/dev/null; echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"from path"}}'`)
	dir := t.TempDir()
	p, err := ParseDefinition("pix", dir, []byte(`{"hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "vulnetix agent hook"}]}]}}`), []string{"vulnetix"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Set{Hooks: p.Hooks, Runner: &Runner{Timeout: 5 * time.Second, MaxBytes: 64 * 1024}}
	if o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"}); o.Decision != DecisionDeny || o.Reasons[0].Text != "from path" {
		t.Fatalf("outcome = %+v", o)
	}
	// A bundle that carries its own file of that name wins as a bundle file, and
	// is then run as a file inside the bundle, never as the allowed program.
	own := bundleDir(t, map[string]string{"vulnetix": "#!/bin/sh\necho own\n"})
	os.Chmod(filepath.Join(own, "vulnetix"), 0o700)
	p, err = ParseDefinition("pix", own, []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "vulnetix"}]}]}}`), []string{"vulnetix"})
	if err != nil || p.Hooks[0].AllowedProgram {
		t.Fatalf("shadowing: %+v %v", p, err)
	}
	// A program found on a PATH entry inside the bundle is refused, even when it is allowed by name.
	inside := bundleDir(t, map[string]string{"bin/tool": "#!/bin/sh\n"})
	os.Chmod(filepath.Join(inside, "bin", "tool"), 0o700)
	t.Setenv("PATH", filepath.Join(inside, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := ParseDefinition("pix", inside, []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "tool x"}]}]}}`), []string{"tool"}); err == nil || !strings.Contains(err.Error(), "inside the bundle") {
		t.Fatalf("a program inside the bundle: %v", err)
	}
}

func jsonString(s string) string {
	b := new(strings.Builder)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
