package agent

import (
	"context"
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

func newWriteSession(t *testing.T, root string, srv *httptest.Server, pol posture.Policy, allowAsk bool) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:      cfg,
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Write{Root: root}),
		Posture:  pol,
		Workdir:  root,
		AllowAsk: allowAsk,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func writeArgs() string { return `{"path":"x.txt","content":"hello"}` }

// TestMutationAskAllowWritesFile drives the interactive gate with a fake
// approver that answers allow: the file must land on disk.
func TestMutationAskAllowWritesFile(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	sess := newWriteSession(t, root, srv, posture.Defaults(), true)

	answered := false
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			if e.AskReply == nil {
				t.Fatal("ask event has no reply channel")
			}
			if e.Ask == nil || e.Ask.Name != "Write" {
				t.Fatalf("ask = %+v, want Write", e.Ask)
			}
			answered = true
			e.AskReply <- PermissionAskReply{Allow: true}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if !answered {
		t.Fatal("no permission ask was emitted")
	}
	body, err := os.ReadFile(filepath.Join(root, "x.txt"))
	if err != nil {
		t.Fatalf("file was not written: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
}

// TestMutationAskDenyLeavesFileUntouched is the deny half: the file must not
// exist after a denied answer.
func TestMutationAskDenyLeavesFileUntouched(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	sess := newWriteSession(t, root, srv, posture.Defaults(), true)

	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			e.AskReply <- PermissionAskReply{Allow: false}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if _, statErr := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file must not exist after deny, stat err = %v", statErr)
	}
}

// TestMutationAskCancelledContextWritesNothing pins that a cancelled context
// mid-ask withholds the call and touches nothing.
func TestMutationAskCancelledContextWritesNothing(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	sess := newWriteSession(t, root, srv, posture.Defaults(), true)

	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	_, _ = sess.run(ctx, nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			once.Do(cancel)
		}
	})
	if _, statErr := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file must not be written, stat err = %v", statErr)
	}
}

// TestMutationNoApproverEnforceWithholds is the non-TTY default: no approver,
// enforce posture withholds naming -allow-ask-without-tty, and nothing writes.
func TestMutationNoApproverEnforceWithholds(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	sess := newWriteSession(t, root, srv, posture.Defaults(), false)

	var events []Event
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if _, statErr := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file must not be written, stat err = %v", statErr)
	}
	sawFlag := false
	for _, e := range events {
		if e.Kind == EventToolResultKind && strings.Contains(e.ToolResult, "-allow-ask-without-tty") {
			sawFlag = true
		}
	}
	if !sawFlag {
		t.Fatal("expected a withheld tool result naming -allow-ask-without-tty")
	}
}

// TestMutationNoApproverAllowWithoutTTYWrites is the -allow-ask-without-tty
// posture: warn/ignore falls through to allow and the write lands.
func TestMutationNoApproverAllowWithoutTTYWrites(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	pol := posture.Defaults()
	pol[posture.PermissionAskNoTTY] = posture.Ignore
	sess := newWriteSession(t, root, srv, pol, false)

	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(Event) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	body, err := os.ReadFile(filepath.Join(root, "x.txt"))
	if err != nil {
		t.Fatalf("file was not written: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
}

// TestConcurrentGroupStopsAtMutatingTool pins 1.2: a Write in the leading run
// ends the concurrent fan-out, so [Read, Write, Read] stops at index 1.
func TestConcurrentGroupStopsAtMutatingTool(t *testing.T) {
	read := &tools.Read{Root: t.TempDir(), MaxBytes: 1024}
	write := &tools.Write{Root: t.TempDir()}
	units := []callUnit{
		{tool: read, decision: permissions.DecisionAllow},
		{tool: write, decision: permissions.DecisionAllow},
		{tool: read, decision: permissions.DecisionAllow},
	}
	if got := concurrentEnd(units); got != 1 {
		t.Fatalf("concurrentEnd = %d, want 1", got)
	}
}

// TestPlanModeWriteWithheldAndUntouched pins 5.1: a plan-mode Write returns the
// withheld string and leaves the file untouched on disk — the string alone does
// not prove the write did not happen.
func TestPlanModeWriteWithheldAndUntouched(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	defer srv.Close()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:      cfg,
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Write{Root: root}),
		Posture:  posture.Defaults(),
		Workdir:  root,
		PlanMode: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []Event
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if _, statErr := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file must not be written in plan mode, stat err = %v", statErr)
	}
	sawWithheld := false
	for _, e := range events {
		if e.Kind == EventToolResultKind && strings.Contains(e.ToolResult, "not allowed in plan mode") {
			sawWithheld = true
		}
	}
	if !sawWithheld {
		t.Fatal("expected a plan-mode withheld result")
	}
}
