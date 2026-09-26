//go:build !windows
// +build !windows

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// lockedBuffer is a bytes.Buffer safe for concurrent writes and reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.b.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.b.String()
}

// waitFor polls cond until it returns true or timeout elapses, failing with
// the captured PTY output on timeout.
func waitFor(t *testing.T, out *lockedBuffer, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s; captured output:\n%s", timeout, out.String())
}

// processMock records every chat request and scripts SAFE/AGENT classifier
// answers so the TUI can start and the recovery subagent can run to exactly one
// turn.
type processMock struct {
	mu        sync.Mutex
	chatUsers []string
}

func newProcessMockServer(t *testing.T) (*httptest.Server, *processMock) {
	t.Helper()
	pm := &processMock{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model-list probes (GET /v1/models or /models).
		if r.Method == http.MethodGet && (strings.Contains(r.URL.Path, "models") || r.URL.Path == "/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"test","object":"model"}]}`))
			return
		}

		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var system, user string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			}
		}

		switch {
		case strings.Contains(system, "security classifier"):
			writeChat(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeChat(w, "AGENT")
		case strings.Contains(system, "clarification questionnaire"):
			writeChat(w, `{"groups":[]}`)
		default:
			pm.mu.Lock()
			pm.chatUsers = append(pm.chatUsers, user)
			pm.mu.Unlock()
			// A prose-only response: the subagent should not call any tool,
			// so this recovery turn is exactly one model request.
			writeChat(w, "restart the process with the same command")
		}
	}))
	return srv, pm
}

// TestProcessStartAndRecovery drives the built binary through a PTY, types
// `!!false`, and asserts that a project process entry is created and the
// recovery subagent dispatches exactly one model turn.
func TestProcessStartAndRecovery(t *testing.T) {
	srv, pm := newProcessMockServer(t)
	defer srv.Close()

	dir := t.TempDir()
	home := t.TempDir()

	global := `{"guardrails":false,"resilience":{"max_process_recoveries":1}}`
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(global), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	cmd := exec.Command(belaiBin, "-trust-dir", "-provider", "openai", "-model", "test")
	cmd.Dir = dir
	// The TUI is skipped when CI is set (cmd/belai/main.go's interactive()
	// gate), and GitHub Actions sets CI=true. Strip it — and BELAI_NO_TUI —
	// so the PTY actually starts the TUI, then add the test's own overrides.
	env := make([]string, 0, len(os.Environ())+4)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CI=") || strings.HasPrefix(e, "BELAI_NO_TUI=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env,
		"BELAI_BASE_URL="+srv.URL,
		"OPENAI_API_KEY=test",
		"BELAI_HOME="+home,
		"TERM=xterm",
	)

	pty, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer pty.Close()
	defer tty.Close()

	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty

	out := &lockedBuffer{}
	go func() {
		_, _ = io.Copy(out, pty)
	}()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start belai: %v", err)
	}

	// Wait for the TUI to render its first frame before typing: on a cold CI
	// runner startup can outlive a fixed sleep, and input sent before the
	// reader attaches can have its Enter consumed by a terminal query.
	waitFor(t, out, 10*time.Second, func() bool {
		return strings.Contains(out.String(), "\n")
	})
	// A short settle after the first output line lets the TUI finish
	// installing its raw-mode input reader before we type.
	time.Sleep(500 * time.Millisecond)

	// Type the command; keep Enter separate so a dropped carriage return can
	// be retried without retyping the command.
	if _, err := pty.Write([]byte("!!false")); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if _, err := pty.Write([]byte("\r")); err != nil {
		t.Fatalf("write enter: %v", err)
	}

	// Poll for the project process entry; retry Enter once after a grace
	// period in case the first was consumed during startup.
	procDir := filepath.Join(dir, ".vulnetix", "processes")
	typed := time.Now()
	retried := false
	waitFor(t, out, 10*time.Second, func() bool {
		entries, err := os.ReadDir(procDir)
		if err == nil && len(entries) == 1 {
			return true
		}
		if !retried && time.Since(typed) > 3*time.Second {
			retried = true
			_, _ = pty.Write([]byte("\r"))
		}
		return false
	})

	// Give the process time to exit and the recovery subagent to fire.
	time.Sleep(3 * time.Second)

	// Ctrl-D starts quit confirmation in the TUI; a second Ctrl-D confirms.
	if _, err := pty.Write([]byte{0x04}); err != nil {
		t.Fatalf("write first ctrl-d: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := pty.Write([]byte{0x04}); err != nil {
		t.Fatalf("write second ctrl-d: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 130 {
				// Treat ctrl-c/-d induced exit as acceptable.
			} else {
				t.Fatalf("belai exited unexpectedly: %v", err)
			}
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Logf("captured output:\n%s", out.String())
		t.Fatal("belai did not exit after ctrl-d")
	}

	// The `!!false` command should have created a project process entry.
	entries, err := os.ReadDir(procDir)
	if err != nil {
		t.Logf("captured output:\n%s", out.String())
		t.Fatalf("read process dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one process entry, got %v", entries)
	}
	body, err := os.ReadFile(filepath.Join(procDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read process entry: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "false" {
		t.Fatalf("process entry body = %q, want \"false\"", got)
	}

	// The recovery subagent should have sent exactly one chat turn.
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if len(pm.chatUsers) != 1 {
		t.Fatalf("expected exactly one recovery chat turn, got %d: %v", len(pm.chatUsers), pm.chatUsers)
	}
	if !strings.Contains(pm.chatUsers[0], "false") {
		t.Fatalf("recovery briefing did not mention the command: %q", pm.chatUsers[0])
	}
}
