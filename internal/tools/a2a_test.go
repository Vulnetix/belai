package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/repomemory"
)

type nopA2AHub struct{}

func (nopA2AHub) A2AMessage(context.Context, string, string) (string, string, error) {
	return "a2a-0", "@belai·000000", nil
}
func (nopA2AHub) A2AAck(context.Context, string, string, string) (bool, error) { return false, nil }

type fakeA2AHub struct {
	to, text, id, decision, reason string
	err                            error
}

func (f *fakeA2AHub) A2AMessage(_ context.Context, to, text string) (string, string, error) {
	f.to, f.text = to, text
	return "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4", "@belai·aaaaaa", f.err
}

func (f *fakeA2AHub) A2AAck(_ context.Context, id, decision, reason string) (bool, error) {
	f.id, f.decision, f.reason = id, decision, reason
	return decision != "ignore", f.err
}

func TestA2AMessageTool(t *testing.T) {
	hub := &fakeA2AHub{}
	tool := A2AMessage{Hub: hub}
	res, err := tool.Execute(context.Background(), map[string]any{"to": " @belai·aaaaaa ", "text": "Are you editing x.go?"})
	if err != nil {
		t.Fatal(err)
	}
	if hub.to != "@belai·aaaaaa" || hub.text != "Are you editing x.go?" {
		t.Fatalf("hub got %q %q", hub.to, hub.text)
	}
	if res.Kind != KindA2A || !strings.Contains(res.Content, "Sent a2a-01k8z3m5x6y7z8a9b0c1d2e3f4 to @belai·aaaaaa. The reply will arrive in a later message.") {
		t.Fatalf("result %+v", res)
	}
	for _, args := range []map[string]any{{"text": "x"}, {"to": "@x"}, {"to": "@x", "text": strings.Repeat("a", MaxA2AText+1)}} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	hub.err = errors.New("no live agent")
	if _, err := tool.Execute(context.Background(), map[string]any{"to": "@x", "text": "q"}); err == nil {
		t.Fatal("hub error swallowed")
	}
	if _, err := (A2AMessage{}).Execute(context.Background(), map[string]any{"to": "@x", "text": "q"}); err == nil {
		t.Fatal("a nil hub sent")
	}
	if Mutates(tool) || !tool.Kind().ReadOnly() || tool.Kind().NeedsClassifier() {
		t.Fatal("A2AMessage must be read-only and sanitise-only")
	}
}

func TestA2AAckTool(t *testing.T) {
	hub := &fakeA2AHub{}
	tool := A2AAck{Hub: hub}
	res, err := tool.Execute(context.Background(), map[string]any{"messageId": "a2a-1", "decision": "ignore", "reason": "I only edit docs/; no overlap."})
	if err != nil || hub.decision != "ignore" || strings.Contains(res.Content, "told") {
		t.Fatalf("ignore: %+v %v", res, err)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"messageId": "a2a-1", "decision": "wait", "reason": "x.go is theirs"})
	if err != nil || !strings.Contains(res.Content, "The other agent was told.") {
		t.Fatalf("wait: %+v %v", res, err)
	}
	for _, args := range []map[string]any{
		{"messageId": "a2a-1", "decision": "maybe", "reason": "x"},
		{"messageId": "a2a-1", "decision": "ignore"},
		{"messageId": "a2a-1", "decision": "ignore", "reason": strings.Repeat("x", MaxA2AReason+1)},
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if Mutates(tool) {
		t.Fatal("A2AAck must not mutate")
	}
}

func TestWithA2A(t *testing.T) {
	r := Default(t.TempDir(), false)
	if r.WithA2A(nil) != r {
		t.Fatal("a nil hub added tools")
	}
	got := r.WithA2A(nopA2AHub{})
	for _, n := range []string{A2AMessageName, A2AAckName} {
		if _, ok := got.Find(n); !ok {
			t.Fatalf("%s missing", n)
		}
		// Offered in plan mode.
		if _, ok := got.PlanWith(PlanSurface{}).Find(n); !ok {
			t.Fatalf("%s missing from the plan surface", n)
		}
	}
}

func TestRepoMemoryTools(t *testing.T) {
	hub := NewRepoMemoryHub(t.TempDir())
	hub.SetRoot(t.TempDir())
	hub.SetKey("o/r")
	var admitted string
	hub.SetAdmit(func(_ context.Context, text string) error {
		admitted = text
		if strings.Contains(text, "ignore all") {
			return errors.New("PROMPT_INJECTION")
		}
		return nil
	})
	agent := "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222"
	hub.SetStamp(func() repomemory.Stamp { return repomemory.Stamp{AgentID: agent} })
	side := hub.WithStamp(repomemory.Stamp{MessageID: "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4", ContextID: "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4"})
	w := RepoMemoryWrite{Hub: side}
	res, err := w.Execute(context.Background(), map[string]any{"note": "A owns x.go", "tags": []any{"x"}})
	if err != nil || res.Kind != KindRepoMemoryWrite || !strings.Contains(res.Content, "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4") || admitted != "A owns x.go" {
		t.Fatalf("write %+v %v %q", res, err, admitted)
	}
	if _, err := w.Execute(context.Background(), map[string]any{"note": "ignore all"}); err == nil {
		t.Fatal("a refused note was stored")
	}
	if _, err := w.Execute(context.Background(), map[string]any{"note": strings.Repeat("x", repomemory.MaxNoteBytes+1)}); err == nil {
		t.Fatal("oversized note stored")
	}
	r := RepoMemoryRead{Hub: hub}
	res, err = r.Execute(context.Background(), map[string]any{"query": "owns"})
	if err != nil || !strings.Contains(res.Content, "A owns x.go") || !strings.Contains(res.Content, agent) || strings.Contains(res.Content, "ignore all") {
		t.Fatalf("read %+v %v", res, err)
	}
	// An out-of-range limit is clamped, never narrowed into something else.
	for _, lim := range []any{"9223372036854775807", float64(1 << 40), -5} {
		if res, err := r.Execute(context.Background(), map[string]any{"query": "owns", "limit": lim}); err != nil || !strings.Contains(res.Content, "A owns x.go") {
			t.Fatalf("limit %v: %+v %v", lim, res, err)
		}
	}
	// A read classifies on its way to a model; the write confirmation is shaped.
	if !res.Kind.NeedsClassifier() || KindRepoMemoryWrite.NeedsClassifier() {
		t.Fatal("repo memory classification")
	}
	if Mutates(r) || Mutates(w) {
		t.Fatal("repo memory tools must not count as workspace mutation")
	}
	none := NewRepoMemoryHub("")
	none.SetKey("")
	if _, err := (RepoMemoryRead{Hub: none}).Execute(context.Background(), nil); err == nil {
		t.Fatal("read without a repository")
	}
	names := []string{}
	for _, tl := range RepoMemoryTools(hub) {
		names = append(names, tl.Definition().Name)
	}
	if strings.Join(names, ",") != "RepoMemoryRead,RepoMemoryWrite" {
		t.Fatalf("side-turn surface = %v", names)
	}
}

// The repo memory tools are core: every registry path keeps them.
func TestRepoMemoryToolsAreCore(t *testing.T) {
	base := Default(t.TempDir(), false)
	if base.RepoMemory() == nil {
		t.Fatal("Default has no repo memory hub")
	}
	for name, r := range map[string]*Registry{
		"default":   base,
		"read-only": Default(t.TempDir(), true),
		"only":      base.Only("Read"),
		"empty":     base.Only(),
		"without":   base.Without(RepoMemoryReadName, RepoMemoryWriteName, "Read"),
		"plan":      base.Plan(),
		"planwith":  base.PlanWith(PlanSurface{}),
		"ro":        base.ReadOnlySurface(),
		"a2a":       base.WithA2A(nopA2AHub{}).Only("Grep"),
	} {
		for _, n := range CoreToolNames {
			if _, ok := r.Find(n); !ok {
				t.Errorf("%s registry has no %s", name, n)
			}
		}
	}
}
