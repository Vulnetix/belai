package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestOffloadedBashOutputReadsBack drives the built binary through a Bash
// call that prints ~110 KB (Bash captures the first and last 32 KiB of it).
// The next request carries a head-and-tail preview,
// not the output; ReadResult returns the exact line asked for; the preview is
// byte-identical on the request after, so the cached prefix holds; and
// -usage-json records the run by role.
func TestOffloadedBashOutputReadsBack(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var toolTurns [][]string // per main request, the tool messages' content
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, user string
		var tools []string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			case "tool":
				tools = append(tools, m.Content)
			}
		}
		switch {
		case strings.Contains(system, "operating-mode classifier"):
			writeChat(w, "AGENT")
			return
		case strings.Contains(system, "security classifier"):
			writeChat(w, securitySentinelFor(user))
			return
		case !strings.Contains(system, "Belai"):
			// Any other role-manager call.
			writeChat(w, "OK")
			return
		}
		mu.Lock()
		toolTurns = append(toolTurns, tools)
		mu.Unlock()
		switch len(tools) {
		case 0:
			writeToolCallChat(w, "Bash", map[string]any{"command": "seq 1 20000"})
		case 1:
			writeToolCallChat(w, "ReadResult", map[string]any{"ref": "r1", "pattern": "^1234$", "context": 0})
		case 2:
			writeToolCallChat(w, "ReadResult", map[string]any{"ref": "r1", "pattern": "^1999[89]$", "context": 0})
		default:
			writeChat(w, "done")
		}
	}))
	defer srv.Close()

	home := filepath.Join(t.TempDir(), "belai-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := filepath.Join(t.TempDir(), "usage.json")
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, "-trust-dir", "-ask-permission=false", "-provider", "openai", "-model", "test",
		"-mode", "agent", "-usage-json", usage, "-prompt", "count to 20000")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test", "BELAI_HOME="+home)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v (stderr %q)", err, errb.String())
	}
	if !strings.Contains(out.String(), "done") {
		t.Fatalf("stdout = %q", out.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(toolTurns) < 4 {
		t.Fatalf("main requests = %d, want 4", len(toolTurns))
	}
	preview := toolTurns[1][0]
	if !strings.Contains(preview, "Offloaded Bash output") || !strings.Contains(preview, `ReadResult(ref="r1")`) {
		t.Fatalf("no preview: %q", preview[:min(len(preview), 300)])
	}
	if !strings.Contains(preview, "\n1\n") && !strings.HasPrefix(preview, "1\n") {
		t.Fatal("the preview lost the head")
	}
	if !strings.Contains(preview, "20000") {
		t.Fatal("the preview lost the tail")
	}
	if strings.Contains(preview, "\n10000\n") {
		t.Fatal("the middle of the output rode on the request")
	}
	if len(preview) > 12*1024 {
		t.Fatalf("preview is %d bytes", len(preview))
	}
	if got := toolTurns[2][1]; !strings.Contains(got, "\t1234\n") {
		t.Fatalf("pattern read back %q", got)
	}
	if got := toolTurns[3][2]; !strings.Contains(got, "\t19998\n") || !strings.Contains(got, "\t19999\n") {
		t.Fatalf("window read back %q", got)
	}
	for i := 2; i < len(toolTurns); i++ {
		if toolTurns[i][0] != preview {
			t.Fatalf("request %d changed the preview: the cached prefix would break", i+1)
		}
	}

	raw, err := os.ReadFile(usage)
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	var sum struct {
		Calls  int                        `json:"calls"`
		ByRole map[string]json.RawMessage `json:"by_role"`
		Req    struct {
			ToolResults map[string]int `json:"tool_results"`
		} `json:"agent_request"`
	}
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Calls < 4 || sum.ByRole["agent"] == nil || sum.ByRole["security"] == nil {
		t.Fatalf("usage summary = %s", raw)
	}
	if sum.Req.ToolResults["Bash"] == 0 || sum.Req.ToolResults["ReadResult"] == 0 {
		t.Fatalf("request composition = %+v", sum.Req.ToolResults)
	}
}
