package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/forge"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/readindex"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// prefetchRepo builds a git repository whose working tree exercises every
// prefetch rule: an instruction file, a modified and an untracked file (both
// attached), and a large file, a deleted file, an untracked directory and a
// file the classifier rejects (none attached).
func prefetchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("AGENTS.md", "AGENTS-BYTES\n")
	write("a.go", "package a\n")
	write("gone.go", "package gone\n")
	write("evil.go", "package evil\n")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	write("a.go", "package a // A-BYTES\n")
	write("new.go", "package n // NEW-BYTES\n")
	write("big.txt", strings.Repeat("BIG-BYTES ", 6000))
	write("evil.go", "package evil // EVIL-MARKER\n")
	write("sub/x.go", "package x // SUBDIR-BYTES\n")
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	return root
}

// prefetchServer is a fake provider: the classifier rejects EVIL-MARKER and
// admits everything else; the main model's user messages are recorded and it
// answers with a Read of a.go once, then plain text.
type prefetchServer struct {
	mu        sync.Mutex
	users     [][]string // user messages of each main-model request
	toolTurns [][]string // tool messages of each main-model request
	readSent  bool
}

func (p *prefetchServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	system, lastUser := "", ""
	var users, toolMsgs []string
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system = m.Content
		case "user":
			lastUser = m.Content
			users = append(users, m.Content)
		case "tool":
			toolMsgs = append(toolMsgs, m.Content)
		}
	}
	if strings.Contains(system, "security classifier") {
		if strings.Contains(lastUser, "EVIL-MARKER") {
			writeChatJSON(w, "PROMPT_INJECTION")
			return
		}
		writeChatJSON(w, "SAFE")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.Contains(system, "Repository map") || strings.Contains(system, "tools") {
		p.users = append(p.users, users)
		p.toolTurns = append(p.toolTurns, toolMsgs)
	}
	if !p.readSent {
		p.readSent = true
		writeToolCallJSON(w, "Read", `{"file_path":"a.go"}`)
		return
	}
	writeChatJSON(w, "## Summary\nDone.")
}

func prefetchSession(t *testing.T, root string, srv *httptest.Server, cache *forge.Cache) *Session {
	t.Helper()
	m := repomap.Scan(context.Background(), root)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.Default(root, false),
		Perms:         permissions.Settings{},
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		Workdir:       root,
		MaxIterations: 4,
		RepoMap:       &m,
		Forge:         cache,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func TestPlanTurnPrefetchesClassifiedContext(t *testing.T) {
	root := prefetchRepo(t)
	fake := &prefetchServer{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	sess := prefetchSession(t, root, srv, nil)

	var prefetched []string
	_, _ = sess.run(context.Background(), nil, TurnInput{Prompt: "plan the change", ForceMode: modes.ModePlan}, false, func(e Event) {
		if e.Kind == EventPrefetchKind {
			prefetched = e.Paths
		}
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.users) == 0 {
		t.Fatal("no main-model request")
	}
	first := strings.Join(fake.users[0], "\n")
	for _, want := range []string{"AGENTS-BYTES", "A-BYTES", "NEW-BYTES", "Harness-attached for this turn"} {
		if !strings.Contains(first, want) {
			t.Errorf("first request lacks %q", want)
		}
	}
	for _, leak := range []string{"EVIL-MARKER", "BIG-BYTES", "SUBDIR-BYTES"} {
		if strings.Contains(first, leak) {
			t.Errorf("first request carries %q", leak)
		}
	}
	if got := strings.Join(prefetched, ","); got != "AGENTS.md,a.go,new.go" {
		t.Errorf("prefetch event paths = %q", got)
	}
	if _, withheld := sess.flagged.lookup(filepath.Join(root, "evil.go"), "", ""); !withheld {
		t.Error("the rejected file was not flagged like a withheld Read")
	}
	// The model's Read of a prefetched file is answered with a pointer.
	if len(fake.toolTurns) < 2 || len(fake.toolTurns[1]) == 0 {
		t.Fatalf("no Read result reached the model: %q", fake.toolTurns)
	}
	res := fake.toolTurns[1][0]
	if strings.Contains(res, "A-BYTES") || !strings.Contains(res, "harness attached it") {
		t.Errorf("Read of a prefetched file = %q, want the pointer to the attachment", res)
	}
}

func TestAgentTurnDoesNotPrefetchButCarriesForgeFacts(t *testing.T) {
	root := prefetchRepo(t)
	fake := &prefetchServer{readSent: true}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	cache := &forge.Cache{Runner: func(context.Context, string, ...string) ([]byte, error) {
		t.Error("a fresh snapshot must not be re-probed")
		return nil, nil
	}}
	cache.Store(root, forge.Snapshot{Root: root, Branch: "main", Upstream: "origin/main", Ahead: 3, PR: nil}, time.Now())
	sess := prefetchSession(t, root, srv, cache)

	_, _ = sess.run(context.Background(), nil, TurnInput{Prompt: "fix it", ForceMode: modes.ModeAgent}, false, func(Event) {})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.users) == 0 {
		t.Fatal("no main-model request")
	}
	first := strings.Join(fake.users[0], "\n")
	if strings.Contains(first, "AGENTS-BYTES") || strings.Contains(first, "Harness-attached") {
		t.Error("an agent-mode turn prefetched")
	}
	if !strings.Contains(first, "upstream: origin/main (ahead 3, behind 0)") {
		t.Errorf("forge facts missing from the turn status:\n%s", first)
	}
}

// An attachment is live only while its user turn is in the conversation:
// the next turn's history (rebuilt without attachments) reads from disk.
func TestPrefetchedAttachmentLiveness(t *testing.T) {
	body := "     1\tpackage a"
	h := readindex.HashResult(body)
	turns := []run.Turn{{Role: "user", Content: "plan", Attachments: []run.Attachment{{Kind: "file", Label: "a.go", Body: body}}}}
	if !liveResult(turns, h) {
		t.Fatal("the attachment is in the conversation")
	}
	turns[0].Attachments = nil
	if liveResult(turns, h) {
		t.Fatal("a dropped attachment must not read as live")
	}
	turns[0].Attachments = []run.Attachment{{Kind: "shell", Label: "!cat a.go", Body: body}}
	if liveResult(turns, h) {
		t.Fatal("only file attachments vouch for a read")
	}
}

func TestAttachedPathsSkipsUserAttachments(t *testing.T) {
	got := attachedPaths([]run.Attachment{{Kind: "file", Label: "@internal/a.go"}, {Kind: "file", Label: "b.go"}, {Kind: "shell", Label: "!ls"}})
	if !got["internal/a.go"] || !got["b.go"] || len(got) != 2 {
		t.Errorf("%v", got)
	}
}
