package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// A classifier call is attributed to its use case, a security payload (no use
// case) to RoleSecurity, and cache components are carried through.
func TestUsageRoleAndCacheComponents(t *testing.T) {
	events := captureUsage(t, "usage-role-model")
	srv := chatServer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"SAFE"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":80}}}`)
	c := classifierFromConfig(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "usage-role-model"}, srv.Client(), nil)
	if _, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{System: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{System: "s", User: "u", UseCase: rolemanager.UseCaseCompaction}); err != nil {
		t.Fatal(err)
	}
	got := events()
	if len(got) != 2 {
		t.Fatalf("events = %+v, want two", got)
	}
	if got[0].Role != RoleSecurity || got[1].Role != rolemanager.UseCaseCompaction {
		t.Fatalf("roles = %q, %q", got[0].Role, got[1].Role)
	}
	if got[0].Prompt != 100 || got[0].Completion != 1 || got[0].CacheRead != 80 {
		t.Fatalf("components = %+v", got[0])
	}
}

// An untagged call is the agent's, and the request shape splits tool results
// by tool name from the rest of the history.
func TestUsageRequestShape(t *testing.T) {
	if r := usageRole(context.Background()); r != RoleAgent {
		t.Fatalf("untagged role = %q", r)
	}
	turns := []Turn{
		{Role: "user", Content: strings.Repeat("a", 400)},
		{Role: "tool", ToolName: "Bash", Content: strings.Repeat("b", 4000)},
		{Role: "tool", ToolName: "Bash", Content: strings.Repeat("c", 4000)},
		{Role: "tool", ToolName: "Read", Content: strings.Repeat("d", 400)},
	}
	s := requestShape(strings.Repeat("s", 800), turns, 50)
	if s.ToolDefs != 50 || s.System < 200 || s.History < 100 || s.History > 110 {
		t.Fatalf("shape = %+v", s)
	}
	if s.ToolResults["Bash"] < 2000 || s.ToolResults["Read"] < 100 || len(s.ToolResults) != 2 {
		t.Fatalf("tool results = %+v", s.ToolResults)
	}
}

// The summary totals by role and model, sums the request shape over agent
// calls only, and writes JSON with the documented field names.
func TestUsageSummaryWrite(t *testing.T) {
	var sum UsageSummary
	sum.Add(UsageEvent{Provider: "p", Model: "m", Tokens: 100, Prompt: 90, Completion: 10, CacheRead: 60, Role: RoleAgent,
		Request: RequestShape{System: 5, ToolDefs: 7, History: 3, ToolResults: map[string]int{"Bash": 40}}})
	sum.Add(UsageEvent{Provider: "p", Model: "fast", Tokens: 20, Role: RoleSecurity, Estimated: true,
		Request: RequestShape{System: 1000}})
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := sum.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Calls     int `json:"calls"`
		Tokens    int `json:"tokens"`
		CacheRead int `json:"cache_read_tokens"`
		Estimated int `json:"estimated_calls"`
		ByRole    map[string]struct {
			Tokens int `json:"tokens"`
		} `json:"by_role"`
		ByModel map[string]struct {
			Calls int `json:"calls"`
		} `json:"by_model"`
		Request struct {
			System      int            `json:"system"`
			ToolDefs    int            `json:"tool_defs"`
			ToolResults map[string]int `json:"tool_results"`
		} `json:"agent_request"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Calls != 2 || got.Tokens != 120 || got.CacheRead != 60 || got.Estimated != 1 {
		t.Fatalf("totals = %+v", got)
	}
	if got.ByRole["agent"].Tokens != 100 || got.ByRole["security"].Tokens != 20 || got.ByModel["p/fast"].Calls != 1 {
		t.Fatalf("breakdown = %s", raw)
	}
	if got.Request.System != 5 || got.Request.ToolDefs != 7 || got.Request.ToolResults["Bash"] != 40 {
		t.Fatalf("agent request = %+v (a security call must not add to it)", got.Request)
	}
	if roles := sum.Roles(); len(roles) != 2 || roles[0] != "agent" {
		t.Fatalf("roles = %v", roles)
	}
}
