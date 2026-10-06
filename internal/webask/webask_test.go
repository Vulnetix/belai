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
