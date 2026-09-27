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

	"github.com/vulnetix/belai/internal/kanban"
)

// TestKanbanWrapUpFilesReportedOpenWork drives the built binary through a work
// turn whose report leaves two things open. The wrap-up after the report files
// both into review, stamped with the run's project and directory, and the
// report stays the reply.
func TestKanbanWrapUpFilesReportedOpenWork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("hello kanban"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := "Read safe.txt. Tests were not run for it; follow-up: update the docs."
	adds := []map[string]any{
		{"title": "Run the tests for safe.txt", "category": "unverified"},
		{"title": "Update the docs for safe.txt", "category": "docs"},
	}
	var mu sync.Mutex
	var wrapUps int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, user string
		wrap, toolsAfterWrap, tools := -1, 0, 0
		for i, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			case "tool":
				tools++
				if wrap >= 0 {
					toolsAfterWrap++
				}
			}
			if strings.Contains(m.Content, kanbanWrapUpPhrase) {
				wrap = i
			}
		}
		switch {
		case strings.Contains(system, "operating-mode classifier"):
			writeChat(w, "AGENT")
		case strings.Contains(system, "security classifier"):
			writeChat(w, securitySentinelFor(user))
		case wrap >= 0:
			mu.Lock()
			wrapUps++
			mu.Unlock()
			if toolsAfterWrap < len(adds) {
				writeToolCallChat(w, "KanbanAdd", adds[toolsAfterWrap])
				return
			}
			writeChat(w, "KANBAN_DONE")
		case tools == 0:
			writeToolCallChat(w, "Read", map[string]any{"path": "safe.txt"})
		default:
			writeChat(w, report)
		}
	}))
	defer srv.Close()

	home := filepath.Join(t.TempDir(), "belai-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"allow":["Read"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, "-trust-dir", "-provider", "openai", "-model", "test", "-prompt", "read safe.txt")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test", "BELAI_HOME="+home)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v (stderr %q)", err, errb.String())
	}
	if !strings.Contains(out.String(), report) || strings.Contains(out.String(), "KANBAN_DONE") {
		t.Fatalf("stdout = %q, want the report only", out.String())
	}
	if wrapUps == 0 {
		t.Fatal("no wrap-up call reached the model")
	}

	items, err := kanban.Open(filepath.Join(home, "kanban")).Search(kanban.Query{Lists: []kanban.List{kanban.Review}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("review items = %d, want 2: %+v", len(items), items)
	}
	abs, _ := filepath.EvalSymlinks(dir)
	for _, it := range items {
		if it.SessionID == "" || it.Project != filepath.Base(dir) {
			t.Errorf("provenance: %+v", it)
		}
		if got, _ := filepath.EvalSymlinks(it.Dir); got != abs {
			t.Errorf("dir = %q, want %q", it.Dir, abs)
		}
	}
}
