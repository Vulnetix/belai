package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// ---------------------------------------------------------------------------
// decidePermission: the permission_no_match posture gate
// ---------------------------------------------------------------------------

func enforceNoMatchPolicy() posture.Policy {
	p := posture.Defaults()
	p[posture.PermissionNoMatch] = posture.Enforce
	return p
}

func TestDecidePermissionNoRulesDefaultAllows(t *testing.T) {
	s := &Session{perms: permissions.Settings{}, posture: posture.Defaults()}
	dec, rule, _ := s.decidePermission("Read", "hello.txt")
	if dec != permissions.DecisionAllow || rule != "" {
		t.Fatalf("decidePermission = %q/%q, want allow/\"\"", dec, rule)
	}
}

func TestDecidePermissionNoRulesEnforceBlocks(t *testing.T) {
	s := &Session{perms: permissions.Settings{}, posture: enforceNoMatchPolicy()}
	dec, rule, _ := s.decidePermission("Read", "hello.txt")
	if dec != permissions.DecisionBlock || rule != "" {
		t.Fatalf("decidePermission = %q/%q, want block/\"\"", dec, rule)
	}
}

func TestDecidePermissionDenyAlwaysBlocks(t *testing.T) {
	perms := permissions.From(nil, nil, []string{"Read"})
	for name, pol := range map[string]posture.Policy{
		"default": posture.Defaults(),
		"enforce": enforceNoMatchPolicy(),
	} {
		s := &Session{perms: perms, posture: pol}
		dec, rule, _ := s.decidePermission("Read", "hello.txt")
		if dec != permissions.DecisionBlock || rule != "Read" {
			t.Fatalf("%s posture: decidePermission = %q/%q, want block/\"Read\"", name, dec, rule)
		}
	}
}

// An explicit allow rule is unaffected by the enforce gate: the gate only
// applies to calls that match no rule.
func TestDecidePermissionAllowRuleBeatsEnforceGate(t *testing.T) {
	perms := permissions.From([]string{"Read"}, nil, nil)
	s := &Session{perms: perms, posture: enforceNoMatchPolicy()}
	dec, rule, _ := s.decidePermission("Read", "hello.txt")
	if dec != permissions.DecisionAllow || rule != "Read" {
		t.Fatalf("decidePermission = %q/%q, want allow/\"Read\"", dec, rule)
	}
}

// ---------------------------------------------------------------------------
// Integration: unmatched tools execute by default; enforce posture withholds
// ---------------------------------------------------------------------------

// toolRecordingServer emulates the provider for a single Read tool call and
// records the content of every tool-role message it receives, so tests can
// distinguish "tool ran" from "tool withheld" (a plain final reply cannot).
type toolRecordingServer struct {
	mu    sync.Mutex
	tools []string
	srv   *httptest.Server
}

func newToolRecordingServer(t *testing.T, path string) *toolRecordingServer {
	t.Helper()
	trs := &toolRecordingServer{}
	trs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		var system string
		toolMsgs := []string{}
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "tool" {
				trs.mu.Lock()
				trs.tools = append(trs.tools, m.Content)
				trs.mu.Unlock()
				toolMsgs = append(toolMsgs, m.Content)
			}
		}

		content := "done"
		var toolCalls any
		switch {
		case strings.Contains(system, "security classifier"):
			content = "SAFE"
		case strings.Contains(system, "operating-mode classifier"):
			content = "AGENT"
		case len(toolMsgs) == 0:
			content = ""
			args, _ := json.Marshal(map[string]any{"path": path})
			toolCalls = []any{map[string]any{
				"id":       "call_1",
				"type":     "function",
				"function": map[string]any{"name": "Read", "arguments": string(args)},
			}}
		}

		msg := map[string]any{"role": "assistant", "content": content}
		if toolCalls != nil {
			msg["tool_calls"] = toolCalls
		}
		b, _ := json.Marshal(map[string]any{
			"id":     "x",
			"object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(trs.srv.Close)
	return trs
}

func (trs *toolRecordingServer) toolContents() string {
	trs.mu.Lock()
	defer trs.mu.Unlock()
	return strings.Join(trs.tools, "\n")
}

func testSessionWithRead(t *testing.T, root string, pol posture.Policy, srv *httptest.Server) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:      cfg,
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:  pol,
		Workdir:  root,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func TestUnmatchedToolExecutesByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("secret-tool-content-42"), 0o600); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}
	trs := newToolRecordingServer(t, "hello.txt")
	sess := testSessionWithRead(t, root, posture.Defaults(), trs.srv)

	res, err := sess.Run(context.Background(), "read the file")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q, want done", res.Reply)
	}
	contents := trs.toolContents()
	if !strings.Contains(contents, "secret-tool-content-42") {
		t.Fatalf("real tool content not forwarded to model: %q", contents)
	}
	if strings.Contains(contents, "tool result withheld") {
		t.Fatalf("no tool result should be withheld by default: %q", contents)
	}
}

func TestEnforcePostureWithholdsUnmatchedTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("secret-tool-content-42"), 0o600); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}
	trs := newToolRecordingServer(t, "hello.txt")
	sess := testSessionWithRead(t, root, enforceNoMatchPolicy(), trs.srv)

	res, err := sess.Run(context.Background(), "read the file")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q, want done", res.Reply)
	}
	contents := trs.toolContents()
	if !strings.Contains(contents, "tool result withheld: permission denied") {
		t.Fatalf("expected withheld tool result under enforce posture: %q", contents)
	}
	if strings.Contains(contents, "secret-tool-content-42") {
		t.Fatalf("tool content must not reach the model under enforce posture: %q", contents)
	}
}
