package webask

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
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

func TestParsePlanChoice(t *testing.T) {
	for _, c := range []struct {
		in        string
		choice    string
		notes     string
		wantError bool
	}{
		{`{"choice":"approve_here"}`, PlanApproveHere, "", false},
		{`{"choice":"approve_new"}`, PlanApproveNew, "", false},
		{`{"choice":"stay","notes":"ignored"}`, PlanStay, "", false},
		{`{"choice":"refine","notes":"  add tests  "}`, PlanRefine, "add tests", false},
		{`{"choice":"refine","notes":"  "}`, "", "", true},
		{`{"choice":"refine"}`, "", "", true},
		{`{"choice":"rm -rf"}`, "", "", true},
		{`not json`, "", "", true},
	} {
		choice, notes, err := ParsePlanChoice(json.RawMessage(c.in))
		if (err != nil) != c.wantError || choice != c.choice || notes != c.notes {
			t.Errorf("ParsePlanChoice(%s) = %q, %q, %v; want %q, %q, error=%v", c.in, choice, notes, err, c.choice, c.notes, c.wantError)
		}
	}
}

func TestPlanReviewAskCapsThePlanText(t *testing.T) {
	content, meta := PlanReviewAsk("p", "/x/p.md", strings.Repeat("a", MaxPlanBytes+10), []string{PlanApproveHere})
	if content != "plan written: /x/p.md" {
		t.Fatalf("content = %q", content)
	}
	if got := meta["plan"].(string); len(got) != MaxPlanBytes || meta["plan_truncated"] != true {
		t.Fatalf("plan len = %d, truncated = %v", len(got), meta["plan_truncated"])
	}
	if _, meta := PlanReviewAsk("p", "/x/p.md", "", nil); meta["plan"] != nil {
		t.Fatalf("an unreadable plan must carry no text: %v", meta)
	}
}
