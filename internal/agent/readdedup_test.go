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

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/readindex"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// The harness answers a repeated read of an unchanged file with a pointer to
// the earlier result — no second copy of the bytes and no second classifier
// round — and a read after the harness changed the file sees the new bytes.
func TestRepeatedReadIsAnsweredFromTheIndexUntilTheFileChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("ORIGINAL-BYTES\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := []struct{ name, args string }{
		{"Read", `{"file_path":"f.txt"}`},
		{"Read", `{"file_path":"f.txt"}`},
		{"Write", `{"file_path":"f.txt","content":"NEW-BYTES\n"}`},
		{"Read", `{"file_path":"f.txt"}`},
	}
	var mu sync.Mutex
	step := 0
	var toolResults []string
	classified := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, lastUser := "", ""
		var tools []string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				lastUser = m.Content
			case "tool":
				tools = append(tools, m.Content)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(system, "security classifier") {
			for _, marker := range []string{"ORIGINAL-BYTES", "NEW-BYTES"} {
				if strings.Contains(lastUser, marker) {
					classified[marker]++
				}
			}
			writeChatJSON(w, "SAFE")
			return
		}
		toolResults = tools
		if step < len(script) {
			c := script[step]
			step++
			writeToolCallJSON(w, c.name, c.args)
			return
		}
		writeChatJSON(w, "done")
	}))
	defer srv.Close()

	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.Default(root, false),
		Perms:         permissions.Settings{Allow: []string{"Write"}},
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		Workdir:       root,
		MaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "read it", ForceMode: modes.ModeAgent}, false, func(Event) {}); err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(toolResults) != 4 {
		t.Fatalf("tool results = %d, want 4: %q", len(toolResults), toolResults)
	}
	if !strings.Contains(toolResults[0], "ORIGINAL-BYTES") {
		t.Fatalf("first read = %q, want the file", toolResults[0])
	}
	if strings.Contains(toolResults[1], "ORIGINAL-BYTES") || !strings.Contains(toolResults[1], "unchanged since you read it") {
		t.Fatalf("second read = %q, want the pointer to the earlier result", toolResults[1])
	}
	if !strings.Contains(toolResults[3], "NEW-BYTES") {
		t.Fatalf("read after the write = %q, want the new bytes (write: %q)", toolResults[3], toolResults[2])
	}
	if classified["ORIGINAL-BYTES"] != 1 {
		t.Fatalf("the unchanged file was classified %d times, want once", classified["ORIGINAL-BYTES"])
	}
}

// Liveness follows the delivered bytes: a result cleared from context is not
// live, and a reused call id on another result does not make it live.
func TestLiveToolResultFollowsContentNotCallID(t *testing.T) {
	result := "     1\tpackage a"
	h := readindex.HashResult(result)
	turns := []run.Turn{{Role: "tool", ToolCallID: "call_1", Content: result}}
	if !liveResult(turns, h) {
		t.Fatal("the delivered result is in the conversation")
	}
	turns[0].Content = run.ClearedToolResult
	turns = append(turns, run.Turn{Role: "tool", ToolCallID: "call_1", Content: "something else"})
	if liveResult(turns, h) {
		t.Fatal("a cleared result with its id reused elsewhere must not read as live")
	}
}
