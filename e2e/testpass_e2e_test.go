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
)

// testPassRepo is a throwaway git repository with one untracked file, so the
// working tree is dirty and a session-level pass has something to test.
func testPassRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// testPassMock answers the classifiers SAFE and everything else "mock reply",
// and records every request body so a test can see what reached the model.
func testPassMock(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		system := ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		switch {
		case strings.Contains(system, "security classifier"):
			writeChat(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeChat(w, "AGENT")
		default:
			writeChat(w, "mock reply")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

// runTestPass runs one headless prompt in dir with the given global settings
// and an optional project settings file, and returns stdout, stderr and the
// exit code.
func runTestPass(t *testing.T, srv *httptest.Server, dir, globalSettings, projectSettings string) (string, string, int) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "belai-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if globalSettings != "" {
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(globalSettings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if projectSettings != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".vulnetix"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".vulnetix", "settings.json"), []byte(projectSettings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, "-trust-dir", "-provider", "openai", "-model", "test", "-prompt", "hi")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test", "BELAI_HOME="+home)
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run belai: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// The pass is the user's opt-in: with no tests block nothing runs.
func TestTestPassIsOffByDefault(t *testing.T) {
	srv, _ := testPassMock(t)
	dir := testPassRepo(t)
	_, errOut, code := runTestPass(t, srv, dir, "", "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if strings.Contains(errOut, "tests") && strings.Contains(errOut, "running tests") {
		t.Fatalf("the pass ran without an opt-in: %q", errOut)
	}
}

// A session-level pass runs the user's command after the run, decides pass by
// the exit code, prints the result and report on stderr, and leaves stdout as
// the reply alone.
func TestTestPassRunsAtSessionEndAndReportsOnStderr(t *testing.T) {
	srv, _ := testPassMock(t)
	dir := testPassRepo(t)
	global := `{"tests":{"post_end":"session","command":["sh","-c","echo ran > ran.txt"]}}`
	out, errOut, code := runTestPass(t, srv, dir, global, "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err != nil {
		t.Fatalf("the suite did not run: %v (stderr %q)", err, errOut)
	}
	if !strings.Contains(errOut, "running tests") || !strings.Contains(errOut, "tests pass") {
		t.Fatalf("stderr = %q, want the run and its result", errOut)
	}
	if strings.Contains(out, "tests pass") || strings.Contains(out, "running tests") {
		t.Fatalf("test lines leaked onto stdout: %q", out)
	}
	if !strings.Contains(out, "mock reply") {
		t.Fatalf("stdout = %q, want the reply", out)
	}
}

// A clean working tree never spends a suite at session end.
func TestTestPassSessionLevelSkipsACleanTree(t *testing.T) {
	srv, _ := testPassMock(t)
	dir := testPassRepo(t)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}
	}
	global := `{"tests":{"post_end":"session","command":["sh","-c","echo ran > ran.txt"]}}`
	_, errOut, code := runTestPass(t, srv, dir, global, "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
		t.Fatalf("a suite ran on a clean tree (stderr %q)", errOut)
	}
}

// A cloned repository cannot turn the pass on or choose its command.
func TestTestPassProjectLayerCannotEnableIt(t *testing.T) {
	srv, _ := testPassMock(t)
	dir := testPassRepo(t)
	project := `{"tests":{"post_end":"session","command":["sh","-c","echo ran > ran.txt"]}}`
	_, errOut, code := runTestPass(t, srv, dir, "", project)
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
		t.Fatalf("a project file enabled the pass (stderr %q)", errOut)
	}
}

// A failing run with on_fail off reports the failure and does not call the
// model with the failure, and the exit code of the run is unchanged.
func TestTestPassFailureWithOnFailOffOnlyReports(t *testing.T) {
	srv, bodies := testPassMock(t)
	dir := testPassRepo(t)
	global := `{"tests":{"post_end":"session","on_fail":"off","command":["sh","-c","echo secret-failure; exit 1"]}}`
	_, errOut, code := runTestPass(t, srv, dir, global, "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "tests fail") {
		t.Fatalf("stderr = %q, want a failing result", errOut)
	}
	for _, b := range bodies() {
		if strings.Contains(b, "The post-end test pass failed") || strings.Contains(b, "secret-failure") {
			t.Fatalf("the failure reached the model with on_fail off: %s", b)
		}
	}
}

// A failing run with on_fail fix hands the failure to the model as a harness
// prompt with the gated output attached, bounded by max_fix_passes.
func TestTestPassFailureStartsTheFixLoop(t *testing.T) {
	srv, bodies := testPassMock(t)
	dir := testPassRepo(t)
	global := `{"tests":{"post_end":"session","on_fail":"fix","max_fix_passes":1,"command":["sh","-c","echo failing-detail; exit 1"]},"resilience":{"max_passes":1}}`
	_, errOut, code := runTestPass(t, srv, dir, global, "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var fixTurns int
	for _, b := range bodies() {
		if strings.Contains(b, "The post-end test pass failed") {
			fixTurns++
			if !strings.Contains(b, "failing-detail") {
				t.Errorf("the gated output was not attached to the fix turn: %s", b)
			}
		}
	}
	if fixTurns == 0 {
		t.Fatalf("no fix turn reached the model (stderr %q)", errOut)
	}
	if !strings.Contains(errOut, "tests failed: fix pass 1 of 1") || !strings.Contains(errOut, "tests fail") {
		t.Fatalf("stderr = %q", errOut)
	}
}
