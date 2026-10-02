package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func rewriteSettings(rules ...config.BashRewriteRule) config.Settings {
	return config.Settings{BashRewrite: &config.BashRewriteSettings{Rules: rules}}
}

// runRewrite runs one turn in which the model sends command as Bash, and
// returns the tool result the model was shown.
func runRewrite(t *testing.T, command string, perms permissions.Settings, settings config.Settings) string {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system := ""
		var toolMsgs []string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "tool":
				toolMsgs = append(toolMsgs, m.Content)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(system, "security classifier") {
			writeChatJSON(w, "SAFE")
			return
		}
		seen = toolMsgs
		if step == 0 {
			step++
			b, _ := json.Marshal(map[string]any{"command": command})
			writeToolCallJSON(w, "Bash", string(b))
			return
		}
		writeChatJSON(w, "done")
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.DefaultWithCaps(root, false, tools.CapabilitiesOf(nil, nil), repoindex.Index{}),
		Perms:         perms,
		Settings:      settings,
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		Workdir:       root,
		MaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "go", ForceMode: modes.ModeAgent}, false, func(Event) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("tool results = %q", seen)
	}
	return seen[0]
}

var fooToEcho = config.BashRewriteRule{Match: "foo", Replace: "echo"}

func TestBashRewriteRunsTheRewrittenLineAndSaysSo(t *testing.T) {
	got := runRewrite(t, "foo hello", permissions.Settings{Allow: []string{"Bash"}}, rewriteSettings(fooToEcho))
	if !strings.HasPrefix(got, "[harness: the user's Bash rewrite rules changed your command") || !strings.Contains(got, "`foo -> echo`") || !strings.Contains(got, "`echo hello`") {
		t.Fatalf("no harness note naming the rule and the line: %q", got)
	}
	if !strings.Contains(got, "\nhello") {
		t.Fatalf("the rewritten command did not run: %q", got)
	}
}

func TestBashRewriteOffByDefaultRunsTheModelsLine(t *testing.T) {
	got := runRewrite(t, "echo hello", permissions.Settings{Allow: []string{"Bash"}}, config.Settings{})
	if strings.Contains(got, "harness:") || !strings.Contains(got, "hello") {
		t.Fatalf("result = %q", got)
	}
}

func TestBashRewriteCannotLaunderADeniedCommand(t *testing.T) {
	// The model's line is denied; the rewrite would turn it into an allowed
	// one. The original is judged first, so it stays denied and unrewritten.
	perms := permissions.Settings{Allow: []string{"Bash"}, Deny: []string{"Bash(foo publish*)"}}
	for _, line := range []string{
		"foo publish",
		"foo ok && foo publish",
		"sudo foo publish",
		"env foo publish",
		"sh -c 'foo publish'",
	} {
		got := runRewrite(t, line, perms, rewriteSettings(fooToEcho))
		if !strings.Contains(got, "permission denied") || strings.Contains(got, "harness: the user's Bash rewrite") {
			t.Errorf("%q: result = %q", line, got)
		}
	}
}

func TestBashRewriteResultIsCheckedAgainstDenyRules(t *testing.T) {
	// The model's line is fine; what it becomes is denied.
	perms := permissions.Settings{Allow: []string{"Bash"}, Deny: []string{"Bash(echo bad*)"}}
	for _, line := range []string{"foo bad", "foo good && foo bad", "foo good; foo bad"} {
		got := runRewrite(t, line, perms, rewriteSettings(fooToEcho))
		if !strings.Contains(got, "permission denied") {
			t.Errorf("%q: result = %q", line, got)
		}
	}
}

func TestBashRewriteResultMustBeCoveredByAllowRulesOnEveryCommand(t *testing.T) {
	s := &Session{
		settings: rewriteSettings(fooToEcho),
		perms:    permissions.Settings{Allow: []string{"Bash(foo*)"}},
	}
	if dec, _ := s.perms.Explain("Bash", "foo a && foo b"); dec != permissions.DecisionAllow {
		t.Fatalf("the allow rule should cover the original, got %v", dec)
	}
	next, _ := s.rewriteBashArgs(map[string]any{"command": "foo a && foo b"})
	cmd, _ := next["command"].(string)
	if cmd != "echo a && echo b" {
		t.Fatalf("rewritten = %q", cmd)
	}
	// With no rule matched the call is not explicitly allowed (it asks).
	if dec, rule := s.perms.Explain("Bash", cmd); dec == permissions.DecisionAllow && rule != "" {
		t.Fatal("an allow rule written for the old command must not cover the new one")
	}
}

func TestBashRewriteLeavesWrappedCommandsAlone(t *testing.T) {
	s := &Session{settings: rewriteSettings(fooToEcho)}
	for _, line := range []string{"env foo hi", "sudo foo hi", "xargs foo", "sh -c 'foo hi'", "echo foo", `printf "%s" foo`} {
		next, note := s.rewriteBashArgs(map[string]any{"command": line})
		if next["command"] != line || note != "" {
			t.Errorf("%q was rewritten to %q", line, next["command"])
		}
	}
}
