package webask

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/planest"
)

func TestPermissionAskRecordsTheRuleAndCapsArgs(t *testing.T) {
	ask := &agent.AskRequest{Name: "Bash", Subject: "make test", Args: map[string]any{"command": strings.Repeat("x", MaxArgsBytes)}}
	content, meta := PermissionAsk(ask)
	if content != "permission: Bash make test" || meta["rule"] != "Bash(make test)" {
		t.Fatalf("content %q rule %v", content, meta["rule"])
	}
	if len(meta["args"].(string)) > MaxArgsBytes {
		t.Fatal("args not capped")
	}
	if Rule(&agent.AskRequest{Name: "Write"}) != "Write" {
		t.Fatal("rule without subject")
	}
}

func TestParseDecision(t *testing.T) {
	for _, ok := range []string{AllowOnce, AllowAlways, Deny} {
		if d, err := ParseDecision(json.RawMessage(`{"decision":"` + ok + `"}`)); err != nil || d != ok {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"decision":"yes"}`, `[]`, `nope`} {
		if _, err := ParseDecision(json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestParseClarifyValidatesAgainstTheOpenQuestions(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		{Options: []clarify.Option{{Label: "a"}, {Label: "b"}}},
		{Multi: true, Options: []clarify.Option{{Label: "c"}, {Label: "d"}}},
	}}
	got, declined, err := ParseClarify(q, json.RawMessage(`{"answers":[{"group":1,"chosen":[0,1,1],"note":"ok\u0007"}]}`))
	if err != nil || declined || len(got.Items) != 2 || !got.Items[0].Skipped || len(got.Items[1].Chosen) != 2 {
		t.Fatalf("%+v %v %v", got, declined, err)
	}
	if strings.ContainsRune(got.Items[1].Note, 7) {
		t.Fatal("note not cleaned")
	}
	for _, bad := range []string{
		`{"answers":[{"group":2}]}`,
		`{"answers":[{"group":0,"chosen":[5]}]}`,
		`{"answers":[{"group":0,"chosen":[0,1]}]}`,
		`{"answers":[{"group":0},{"group":0}]}`,
	} {
		if _, _, err := ParseClarify(q, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	if _, declined, _ := ParseClarify(q, json.RawMessage(`{"decline":true}`)); !declined {
		t.Fatal("decline")
	}
}

func TestParsePlanAnswer(t *testing.T) {
	offered := []PlanModel{
		{Provider: "anthropic", Model: "claude-opus-5-5", Current: true},
		{Provider: "anthropic", Model: "claude-haiku-5-5", Fast: true},
	}
	for _, c := range []struct {
		in        string
		want      PlanAnswer
		wantError bool
	}{
		{`{"choice":"approve_here"}`, PlanAnswer{Choice: PlanApproveHere}, false},
		{`{"choice":"approve_new"}`, PlanAnswer{Choice: PlanApproveNew}, false},
		{`{"choice":"stay","notes":"ignored"}`, PlanAnswer{Choice: PlanStay}, false},
		{`{"choice":"refine","notes":"  add tests  "}`, PlanAnswer{Choice: PlanRefine, Notes: "add tests"}, false},
		{`{"choice":"approve_new","model":{"provider":"anthropic","model":"claude-haiku-5-5"}}`,
			PlanAnswer{Choice: PlanApproveNew, Provider: "anthropic", Model: "claude-haiku-5-5"}, false},
		{`{"choice":"approve_here","model":{"provider":"","model":""}}`, PlanAnswer{Choice: PlanApproveHere}, false},
		{`{"choice":"approve_here","model":{"provider":"evil","model":"x"}}`, PlanAnswer{}, true},
		{`{"choice":"approve_here","model":{"provider":"anthropic"}}`, PlanAnswer{}, true},
		{`{"choice":"refine","notes":"  "}`, PlanAnswer{}, true},
		{`{"choice":"refine"}`, PlanAnswer{}, true},
		{`{"choice":"rm -rf"}`, PlanAnswer{}, true},
		{`not json`, PlanAnswer{}, true},
	} {
		got, err := ParsePlanAnswer(json.RawMessage(c.in), offered)
		if (err != nil) != c.wantError || got != c.want {
			t.Errorf("ParsePlanAnswer(%s) = %+v, %v; want %+v, error=%v", c.in, got, err, c.want, c.wantError)
		}
	}
	if _, err := ParsePlanAnswer(json.RawMessage(`{"choice":"approve_here","model":{"provider":"anthropic","model":"claude-opus-5-5"}}`), nil); err == nil {
		t.Error("a model must be refused when none was offered")
	}
}

func TestPlanReviewAskCarriesTheOffer(t *testing.T) {
	models := make([]PlanModel, MaxPlanModels+5)
	for i := range models {
		models[i] = PlanModel{Provider: "p", Model: fmt.Sprint(i)}
	}
	est := planest.Compute("plan", 100, 0)
	_, meta := PlanReviewAsk("p", "/x/p.md", "body", PlanOffer{Options: []string{PlanApproveHere}, Models: models, Estimate: &est})
	if got := meta["models"].([]PlanModel); len(got) != MaxPlanModels || got[0].Model != "0" {
		t.Fatalf("models = %d entries, want the first %d", len(got), MaxPlanModels)
	}
	if meta["estimates"].(planest.Estimate) != est {
		t.Fatalf("estimates = %v", meta["estimates"])
	}
	if _, meta := PlanReviewAsk("p", "/x/p.md", "body", PlanOffer{}); meta["models"] != nil || meta["estimates"] != nil {
		t.Fatalf("an empty offer must add no models or estimates: %v", meta)
	}
}

func TestPlanReviewAskCapsThePlanText(t *testing.T) {
	content, meta := PlanReviewAsk("p", "/x/p.md", strings.Repeat("a", MaxPlanBytes+10), PlanOffer{Options: []string{PlanApproveHere}})
	if content != "plan written: /x/p.md" {
		t.Fatalf("content = %q", content)
	}
	if got := meta["plan"].(string); len(got) != MaxPlanBytes || meta["plan_truncated"] != true {
		t.Fatalf("plan len = %d, truncated = %v", len(got), meta["plan_truncated"])
	}
	if _, meta := PlanReviewAsk("p", "/x/p.md", "", PlanOffer{}); meta["plan"] != nil {
		t.Fatalf("an unreadable plan must carry no text: %v", meta)
	}
}
