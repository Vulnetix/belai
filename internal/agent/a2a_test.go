package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomemory"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// The side-turn answers on the session's model with exactly the two repo
// memory tools, scoped to this session's repository, and records its calls.
func TestSideReplyHasOnlyRepoMemoryTools(t *testing.T) {
	var mu sync.Mutex
	var advertised [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system string
		var toolMsgs int
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "tool" {
				toolMsgs++
			}
		}
		content := "I hold x.go until the tests pass; take docs/."
		var calls any
		switch {
		case strings.Contains(system, "security classifier"):
			content = "SAFE"
		case strings.Contains(system, "operating-mode classifier"):
			content = "AGENT"
		case len(req.Tools) > 0:
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			sort.Strings(names)
			mu.Lock()
			advertised = append(advertised, names)
			mu.Unlock()
			if toolMsgs == 0 {
				content = ""
				args, _ := json.Marshal(map[string]any{"note": "agreed: B owns docs/, A holds x.go"})
				calls = []any{map[string]any{"id": "c1", "type": "function",
					"function": map[string]any{"name": "RepoMemoryWrite", "arguments": string(args)}}}
			}
		}
		msg := map[string]any{"role": "assistant", "content": content}
		if calls != nil {
			msg["tool_calls"] = calls
		}
		b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	root := t.TempDir()
	reg := tools.Default(root, false)
	memRoot := t.TempDir()
	reg.RepoMemory().SetRoot(memRoot)
	reg.RepoMemory().SetKey("o/r")
	s, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: reg,
		Posture:  posture.Defaults(),
		Workdir:  root,
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.sideSession(reg.RepoMemory())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sub.registry.Names(), ","); got != "RepoMemoryRead,RepoMemoryWrite" {
		t.Fatalf("side-turn registry = %s", got)
	}
	if sub.codeRegistry != nil {
		t.Fatal("the side-turn has a code surface")
	}

	const msgID = "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4"
	res, err := s.SideReply(context.Background(), SideReplyInput{From: "@belai·bbbbbb", Question: "Are you editing x.go?",
		MessageID: msgID, ContextID: msgID, Context: "Task: fix the parser."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "x.go") {
		t.Fatalf("reply = %q", res.Reply)
	}
	if len(res.Calls) != 1 || res.Calls[0].Name != "RepoMemoryWrite" || res.Calls[0].Result == "" {
		t.Fatalf("calls = %+v", res.Calls)
	}
	mu.Lock()
	for _, names := range advertised {
		if strings.Join(names, ",") != "RepoMemoryRead,RepoMemoryWrite" {
			t.Errorf("the side-turn was offered %v", names)
		}
	}
	mu.Unlock()
	store, _ := repomemory.Open(memRoot, "o/r")
	notes, _ := store.Search("owns", 0)
	if len(notes) != 1 || notes[0].MessageID != msgID {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestPeerNotesDrain(t *testing.T) {
	s := testSession(t)
	if s.Peer(PeerNote{}) {
		t.Fatal("an empty note was queued")
	}
	s.Peer(PeerNote{Directive: "A2A broadcast a2a-x"})
	s.Peer(PeerNote{Directive: "reply", Attachments: []run.Attachment{{Kind: "file", Label: "A2A reply", Body: "ok"}}})
	turns := s.drainPeers()
	if len(turns) != 4 || turns[0].Directive != "A2A broadcast a2a-x" || len(turns[2].Attachments) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if len(s.TakePeerNotes()) != 0 {
		t.Fatal("notes left after a drain")
	}
	// The default registry's repo memory gate is the session's.
	if !s.registry.RepoMemory().HasAdmit() {
		t.Fatal("repo memory has no classifier gate")
	}
}
