package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveDecisionsProbe sends the routing request, and variants of it, to the
// real OpenRouter Decisions API and logs each raw status and body, so a failing
// shape can be told apart from a failing endpoint. It is a diagnostic, not a
// regression test: it runs only when BELAI_JEV_LIVE=1 and OPENROUTER_API_KEY
// are set, and never retries.
//
//	BELAI_JEV_LIVE=1 OPENROUTER_API_KEY=… go test ./internal/rolemanager/jev -run LiveDecisionsProbe -v
func TestLiveDecisionsProbe(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if os.Getenv("BELAI_JEV_LIVE") != "1" || key == "" {
		t.Skip("set BELAI_JEV_LIVE=1 and OPENROUTER_API_KEY to probe the live Decisions API")
	}
	noul := func(instr string, criteria bool) map[string]any {
		q := map[string]any{"type": "noul", "instructions": instr}
		if criteria {
			q["criteria"] = map[string]string{
				"true":  "The named model is the right fit for this use case.",
				"false": "Another model would serve this use case better.",
			}
		}
		return q
	}
	route := func(criteria bool, state map[string]any, keys ...string) map[string]any {
		qs := map[string]any{}
		models := map[string]string{"main": "anthropic claude-sonnet-5", "mode_eval": "openai gpt-5-mini"}
		for _, k := range keys {
			qs[k] = noul("This use case should be served by "+models[k]+".", criteria)
		}
		return map[string]any{"model": DefaultModel, "questions": qs, "state": state}
	}
	bare := map[string]any{"use_case": "mode_eval"}
	rich := map[string]any{"use_case": "mode_eval", "description": "Classify a user prompt into one of: ask, plan, agent, goal. The reply is a single token."}

	variants := []struct {
		name string
		body map[string]any
	}{
		{"control: tool-call gate shape", map[string]any{
			"model":     DefaultModel,
			"questions": map[string]any{unsafeQuestion: noul(unsafeInstruction, false)},
			"state":     map[string]any{"tool_call": `{"name":"Bash","args":{"command":"ls"}}`},
		}},
		{"route: as sent today (2 questions, bare state)", route(false, bare, "main", "mode_eval")},
		{"route: 1 question, bare state", route(false, bare, "mode_eval")},
		{"route: with criteria", route(true, bare, "main", "mode_eval")},
		{"route: rich state", route(false, rich, "main", "mode_eval")},
		{"route: criteria + rich state", route(true, rich, "main", "mode_eval")},
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, v := range variants {
		raw, _ := json.Marshal(v.body)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, DefaultServerURL+"/api/alpha/decisions", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Logf("%-48s transport error after %s: %v", v.name, time.Since(start).Round(time.Millisecond), err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		t.Logf("%-48s HTTP %d in %s\n  request:  %s\n  response: %s", v.name, resp.StatusCode, time.Since(start).Round(time.Millisecond), raw, body)
	}
}
