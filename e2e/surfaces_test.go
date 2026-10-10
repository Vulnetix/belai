package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// anthropicText joins the text of an Anthropic content field: a plain string, or
// an array of blocks of which only the text blocks count. toolResult reports a
// tool_result block in it.
func anthropicText(raw json.RawMessage) (text string, toolResult bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "tool_result":
			toolResult = true
		}
	}
	return strings.Join(parts, "\n"), toolResult
}

// fleetMockAnthropic is the scripted model in the Anthropic messages shape:
// the system prompt is a string or a list of blocks (the prompt cache), tool
// results come back as blocks of a user message, and a tool call is a tool_use
// block.
func fleetMockAnthropic(t *testing.T) *httptest.Server {
	t.Helper()
	return fleetServer(func(r *http.Request) (system, lastUser string, wrote bool, err error) {
		var req struct {
			System   json.RawMessage `json:"system"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			err = fmt.Errorf("not the Anthropic messages endpoint: %s", r.URL.Path)
			return
		}
		if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
			return
		}
		system, _ = anthropicText(req.System)
		for _, m := range req.Messages {
			text, result := anthropicText(m.Content)
			wrote = wrote || result
			if m.Role == "user" && text != "" {
				lastUser = text
			}
		}
		return
	}, func(w http.ResponseWriter, a fleetAnswer) {
		content := []any{}
		if a.text != "" {
			content = append(content, map[string]any{"type": "text", "text": a.text})
		}
		stop := "end_turn"
		if a.tool != "" {
			content = append(content, map[string]any{"type": "tool_use", "id": "toolu_1", "name": a.tool, "input": a.args})
			stop = "tool_use"
		}
		b, _ := json.Marshal(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "content": content, "stop_reason": stop,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
}

// surface is one wire format the same scripted model is served in.
type surface struct {
	name, provider, keyEnv string
	mock                   func(*testing.T) *httptest.Server
}

var surfaces = []surface{
	{"openai-chat", "openai", "OPENAI_API_KEY", fleetMock},
	{"anthropic-messages", "anthropic", "ANTHROPIC_API_KEY", fleetMockAnthropic},
}

// newFleetEnvOn is newFleetEnv with the model served on the given surface. The
// returned args select its provider for `agent run`.
func newFleetEnvOn(t *testing.T, s surface) (*fleetEnv, []string) {
	t.Helper()
	e := newFleetEnv(t)
	srv := s.mock(t)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	e.keyEnv = s.keyEnv
	return e, []string{"-provider", s.provider, "-model", "test"}
}

// The same scripted model, served in each wire format, takes a card through the
// same loop: a builder from a stored profile, a reviewer that rejects by recording
// an outcome, and a worker made from a prompt alone. A profile therefore behaves
// the same on a Claude model as on one that speaks the OpenAI shape, because the
// harness, not the model's wire format, decides what the run means.
func TestFleetFlowIsTheSameOnEveryWireSurface(t *testing.T) {
	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			e, model := newFleetEnvOn(t, s)
			e.importProfiles()
			e.importRejecter()
			run := func(args ...string) string {
				t.Helper()
				return e.mustBelai(append(append([]string{"agent", "run", "-once"}, model...), args...)...)
			}

			built := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build"))
			e.mustBelai(append(append([]string{"agent", "run", "-trust-dir", "-once"}, model...), "t-builder")...)
			it := e.item(built)
			if it.List != kanban.Review || !strings.HasPrefix(it.Branch, "belai/"+built) || strings.Join(it.Labels, ",") != "needs-review" {
				t.Fatalf("after the builder: %s %q labels %v", it.List, it.Branch, it.Labels)
			}
			if got := e.git("show", it.Branch+":hello.txt"); got != "hello" {
				t.Fatalf("branch content %q", got)
			}

			run("t-rejecter")
			it = e.item(built)
			if it.List != kanban.Backlog || it.Bounces != 1 || it.Attempts != 0 || !strings.Contains(strings.Join(historyNotes(it), "\n"), "outcome failure: hello.txt has no trailing newline") {
				t.Fatalf("after the rejecting reviewer: %s bounces %d attempts %d", it.List, it.Bounces, it.Attempts)
			}

			promptOnly := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt again", "-label", "docs"))
			run("-prompt=WRITER-PERSONA. Implement the attached item.", "-tools=Read,Write,update_plan", "-claim=backlog:docs", "-to=review")
			it = e.item(promptOnly)
			if it.List != kanban.Review || !strings.HasPrefix(it.Branch, "belai/"+promptOnly) {
				t.Fatalf("after the prompt-only worker: %s %q", it.List, it.Branch)
			}
			if got := e.git("show", it.Branch+":hello.txt"); got != "hello" {
				t.Fatalf("prompt-only branch content %q", got)
			}
		})
	}
}

func historyNotes(it kanban.Item) []string {
	var notes []string
	for _, h := range it.History {
		notes = append(notes, h.Note)
	}
	return notes
}
