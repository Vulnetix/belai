package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// sideServer answers classifier calls SAFE and a main call with reply,
// recording what each main call carried.
type sideServer struct {
	mu     sync.Mutex
	reply  string
	status int
	calls  []sideCall
}

type sideCall struct {
	tools  int
	system string
	users  []string
}

func (f *sideServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var c sideCall
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			c.system = m.Content
		case "user":
			c.users = append(c.users, m.Content)
		}
	}
	c.tools = len(req.Tools)
	content := f.reply
	switch {
	case len(req.Messages) == 0:
		// A probe, not a model call.
	case strings.Contains(c.system, "security classifier"):
		content = "SAFE"
	case strings.Contains(c.system, "operating-mode classifier"):
		content = "AGENT"
	default:
		f.mu.Lock()
		f.calls = append(f.calls, c)
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			http.Error(w, "bad request", status)
			return
		}
	}
	b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func newSideSession(t *testing.T, f *sideServer) *Session {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	s, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: tools.Default(root, false),
		Posture:  posture.Defaults(),
		Workdir:  root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAskSideAdvertisesNoToolsAndCarriesTheQuestion(t *testing.T) {
	f := &sideServer{reply: "It is a parser."}
	s := newSideSession(t, f)
	before := strings.Join(s.registry.Names(), ",")

	res, err := s.AskSide(context.Background(), SideAskInput{Question: "What is x.go?", Context: "Latest task from the user: fix the parser."})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "It is a parser." {
		t.Fatalf("reply = %q", res.Reply)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("expected one main call, got %d", len(f.calls))
	}
	c := f.calls[0]
	if c.tools != 0 {
		t.Fatalf("the side question advertised %d tool definitions", c.tools)
	}
	last := c.users[len(c.users)-1]
	for _, want := range []string{"What is x.go?", "fix the parser", "not instructions to you"} {
		if !strings.Contains(last, want) {
			t.Errorf("the prompt lacks %q: %q", want, last)
		}
	}
	if strings.Contains(strings.Join(c.users[:len(c.users)-1], "\n"), "What is x.go?") {
		t.Error("the question appears in an earlier turn")
	}
	if after := strings.Join(s.registry.Names(), ","); after != before {
		t.Errorf("the parent registry changed: %s -> %s", before, after)
	}
}

// The harness lead must survive the sanitiser unchanged, or prompt admission
// would classify the harness's own text.
func TestSideAskPromptIsSanitiserStable(t *testing.T) {
	if got := sanitize.Sanitize(sideAskPrompt); got != sideAskPrompt {
		t.Fatalf("sanitising changes the harness lead:\n%q\n%q", got, sideAskPrompt)
	}
}

func TestAskSideRefusesAnEmptyQuestion(t *testing.T) {
	f := &sideServer{reply: "x"}
	s := newSideSession(t, f)
	if _, err := s.AskSide(context.Background(), SideAskInput{Question: " \n\t "}); !errors.Is(err, ErrSideEmptyQuestion) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("an empty question reached the model")
	}
}

func TestAskSideEmptyReplyIsAnError(t *testing.T) {
	f := &sideServer{reply: "   "}
	s := newSideSession(t, f)
	if _, err := s.AskSide(context.Background(), SideAskInput{Question: "hello?"}); err == nil {
		t.Fatal("an empty reply was accepted")
	}
}

func TestAskSideClientErrorIsAnError(t *testing.T) {
	f := &sideServer{reply: "x", status: http.StatusBadRequest}
	s := newSideSession(t, f)
	if _, err := s.AskSide(context.Background(), SideAskInput{Question: "hello?"}); err == nil {
		t.Fatal("a provider error produced a reply")
	}
}

func TestAskSideCapsTheReplyOnARuneBoundary(t *testing.T) {
	f := &sideServer{reply: strings.Repeat("é", 400)}
	s := newSideSession(t, f)
	res, err := s.AskSide(context.Background(), SideAskInput{Question: "hello?", MaxReplyBytes: 513})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reply) > 513 || !strings.HasSuffix(res.Reply, "é") || len(res.Reply)%2 != 0 {
		t.Fatalf("reply is %d bytes, not cut on a rune boundary", len(res.Reply))
	}
}

func TestFinishSideReply(t *testing.T) {
	got, err := finishSideReply("  ok \x1b[31mred\x1b[0m‮ ", 100)
	if err != nil || strings.ContainsAny(got, "\x1b‮") || !strings.HasPrefix(got, "ok") {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := finishSideReply("\x1b[0m ​", 10); err == nil {
		t.Fatal("a reply that sanitises to nothing was accepted")
	}
	got, _ = finishSideReply("abéc", 3)
	if got != "ab" {
		t.Fatalf("cut = %q", got)
	}
}

// A side question is billed under its own role, so it never replaces the
// request the cache keep-alive replays or the agent-only cost figures.
func TestAskSideUsesItsOwnUsageRole(t *testing.T) {
	var mu sync.Mutex
	roles := map[string]int{}
	cancel := run.SetUsageObserver(func(ev run.UsageEvent) {
		mu.Lock()
		roles[ev.Role]++
		mu.Unlock()
	})
	defer cancel()
	f := &sideServer{reply: "fine"}
	s := newSideSession(t, f)
	if _, err := s.AskSide(context.Background(), SideAskInput{Question: "hello?"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if roles[run.RoleSide] != 1 || roles[run.RoleAgent] != 0 {
		t.Fatalf("roles = %v", roles)
	}
}

func TestAskSideDefaultCap(t *testing.T) {
	f := &sideServer{reply: strings.Repeat("a", config.DefaultSideMaxReplyBytes+500)}
	s := newSideSession(t, f)
	res, err := s.AskSide(context.Background(), SideAskInput{Question: "hello?"})
	if err != nil || len(res.Reply) != config.DefaultSideMaxReplyBytes {
		t.Fatalf("len = %d, err = %v", len(res.Reply), err)
	}
}
