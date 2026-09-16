package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBashEcho(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("expected hello in %q", res.Content)
	}
}

func TestBashRejectsShellMetacharacters(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	for _, meta := range ";&|$`<>()" {
		_, err := b.Execute(context.Background(), map[string]any{"command": "echo " + string(meta)})
		if err == nil {
			t.Fatalf("expected rejection for metacharacter %q", meta)
		}
	}
}

func TestBashNonZeroExitReturnsOutputAndNoError(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "false"})
	if err != nil {
		t.Fatalf("Execute should not error on non-zero exit: %v", err)
	}
	if !strings.Contains(res.Content, "exit status 1") {
		t.Fatalf("expected exit status in %q", res.Content)
	}
}

func TestBashTimeout(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 100 * time.Millisecond}
	res, err := b.Execute(context.Background(), map[string]any{"command": "sleep 5"})
	// A timeout is reported in the content, not as an error: executeCall drops
	// the Result whenever err is non-nil, so returning an error here would
	// throw away everything the command produced before the deadline.
	if err != nil {
		t.Fatalf("timeout should not be an error: %v", err)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("expected timeout notice in %q", res.Content)
	}
}

// TestBashTimeoutKeepsPartialOutput is the reason a timeout stops being an
// error: a command that printed useful work before hanging should not have it
// discarded.
func TestBashTimeoutKeepsPartialOutput(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 300 * time.Millisecond}
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "printf 'partial\\n'; sleep 5",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "partial") {
		t.Fatalf("partial output was discarded: %q", res.Content)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("expected timeout notice in %q", res.Content)
	}
}

// TestBashTimeoutKillsProcessGroup: cancelling `sh` alone leaves a pipeline's
// children running and holding the output pipe open. The whole group has to go.
func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 200 * time.Millisecond}
	start := time.Now()
	if _, err := b.Execute(context.Background(), map[string]any{
		"command": "sleep 30 | cat",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("command outlived its timeout by %s; process group not killed", elapsed)
	}
}

// TestBashDoesNotWaitOnDetachedChild: a backgrounded grandchild inherits the
// output pipe, and Wait blocks until every writer closes. WaitDelay bounds it.
func TestBashDoesNotWaitOnDetachedChild(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 10 * time.Second}
	start := time.Now()
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "(sleep 30 &) ; echo done",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "done") {
		t.Fatalf("expected command output, got %q", res.Content)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Wait blocked %s on a detached child", elapsed)
	}
}

// TestBashExecuteStreamReportsWholeLines pins the sink contract: whole lines
// only, in the order written, and their concatenation is the final content.
func TestBashExecuteStreamReportsWholeLines(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}

	var mu sync.Mutex
	var got []string
	sink := func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p.Text)
	}

	res, err := b.ExecuteStream(context.Background(), map[string]any{
		"command": "printf 'a\\nb\\nc\\n'",
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(got, "\n")
	if joined != "a\nb\nc" {
		t.Fatalf("streamed %q, want %q", joined, "a\nb\nc")
	}
	if strings.TrimRight(res.Content, "\n") != joined {
		t.Fatalf("stream %q and result %q disagree", joined, res.Content)
	}
}

// TestBashExecuteStreamPreservesInterleaving: stdout and stderr share one
// writer precisely so their order survives. Two pipes would reorder this.
func TestBashExecuteStreamPreservesInterleaving(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}

	var mu sync.Mutex
	var got []string
	res, err := b.ExecuteStream(context.Background(), map[string]any{
		"command": "echo out; echo err >&2; echo out2",
	}, func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p.Text)
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if want := "out\nerr\nout2"; strings.TrimRight(res.Content, "\n") != want {
		t.Fatalf("result %q, want %q", res.Content, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "out\nerr\nout2"; strings.Join(got, "\n") != want {
		t.Fatalf("stream %q, want %q", strings.Join(got, "\n"), want)
	}
}

// TestBashExecuteStreamReportsTrailingPartialLine: output with no final
// newline — a prompt, a progress line — must still reach the sink.
func TestBashExecuteStreamReportsTrailingPartialLine(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	var got []string
	if _, err := b.ExecuteStream(context.Background(), map[string]any{
		"command": "printf 'no newline'",
	}, func(p Progress) { got = append(got, p.Text) }); err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if strings.Join(got, "\n") != "no newline" {
		t.Fatalf("streamed %q", got)
	}
}

// TestBashExecuteEqualsExecuteStreamWithNilSink pins the interface contract
// that lets non-streaming callers ignore StreamingTool entirely.
func TestBashExecuteEqualsExecuteStreamWithNilSink(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	args := map[string]any{"command": "echo hello; echo world >&2"}

	a, err := b.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	c, err := b.ExecuteStream(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if a.Content != c.Content || a.Kind != c.Kind {
		t.Fatalf("Execute %+v and ExecuteStream(nil) %+v diverge", a, c)
	}
}

// TestBashImplementsStreamingTool keeps the assertion in executeCall honest.
func TestBashImplementsStreamingTool(t *testing.T) {
	var tool Tool = &Bash{Root: t.TempDir()}
	if _, ok := tool.(StreamingTool); !ok {
		t.Fatal("Bash must implement StreamingTool")
	}
}

func TestBashTruncation(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second, MaxBytes: 5}
	res, err := b.Execute(context.Background(), map[string]any{"command": "printf '123456789'"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 5 bytes") {
		t.Fatalf("expected truncation marker in %q", res.Content)
	}
}

func TestBashConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	b := &Bash{Root: root, ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Content) != root {
		t.Fatalf("expected dir %q, got %q", root, strings.TrimSpace(res.Content))
	}
}

func TestBashScrubsCredentialEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "super-secret")
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "super-secret") {
		t.Fatalf("env output leaked OPENAI_API_KEY")
	}
}

func TestBashResult(t *testing.T) {
	res := BashResult("x")
	if res.Kind != KindBash || res.Content != "x" {
		t.Fatalf("BashResult = %+v", res)
	}
}

func TestBashRejectsNonAllowlisted(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true}
	for _, cmd := range []string{"rm -rf /", "awk '{print > \"x\"}'", "sed -i x", "xargs rm", "git add ."} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err == nil {
			t.Fatalf("Bash(%q) should be rejected", cmd)
		}
	}
}

func TestBashAllowsDataWork(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true}
	for _, cmd := range []string{"echo hi", "cat x", "git status", "find . -name x", "jq . x"} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err != nil && strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("Bash(%q) should be allowlisted, got %v", cmd, err)
		}
	}
}

func TestBashFullModeRunsShellSyntax(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo a && echo b"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Content) != "a\nb" {
		t.Fatalf("chained commands = %q, want a\\nb", res.Content)
	}
	res, err = b.Execute(context.Background(), map[string]any{"command": "echo hi | tr a-z A-Z"})
	if err != nil {
		t.Fatalf("Execute pipe: %v", err)
	}
	if strings.TrimSpace(res.Content) != "HI" {
		t.Fatalf("pipe = %q, want HI", res.Content)
	}
}

func TestBashFullModeRunsNonAllowlisted(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	// touch is not in the read-only allowlist; full mode runs it fine.
	res, err := b.Execute(context.Background(), map[string]any{"command": "touch made.txt && ls made.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "made.txt") {
		t.Fatalf("expected made.txt in %q", res.Content)
	}
}

func TestBashDefinitionBranchesOnMode(t *testing.T) {
	full := (&Bash{Root: t.TempDir()}).Definition()
	if !strings.Contains(full.Description, "full shell") {
		t.Fatalf("full-mode description = %q", full.Description)
	}
	ro := (&Bash{Root: t.TempDir(), ReadOnly: true}).Definition()
	if !strings.Contains(ro.Description, "read-only") {
		t.Fatalf("read-only description = %q", ro.Description)
	}
}

func TestDefaultWiresBashReadOnly(t *testing.T) {
	dir := t.TempDir()
	full, ok := Default(dir, false).Find("Bash")
	if !ok {
		t.Fatalf("Bash not registered")
	}
	if b, ok := full.(*Bash); !ok || b.ReadOnly {
		t.Fatalf("Default(dir, false) should give full-mode Bash, got %v", full)
	}
	ro, ok := Default(dir, true).Find("Bash")
	if !ok {
		t.Fatalf("Bash not registered")
	}
	if b, ok := ro.(*Bash); !ok || !b.ReadOnly {
		t.Fatalf("Default(dir, true) should give read-only Bash, got %v", ro)
	}
}
