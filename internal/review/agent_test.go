package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func TestProfileIsReadOnlyAndKeepsTheReviewerRules(t *testing.T) {
	p, err := Profile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != agentprofile.ModeSingle || p.Kanban != nil || p.Workspace != nil {
		t.Fatalf("profile = %+v", p)
	}
	for _, tool := range []string{"Bash", "Write", "Edit", "PublishBranch", "WebFetch"} {
		if slices.Contains(p.Tools, tool) {
			t.Errorf("the review turn has %s", tool)
		}
	}
	for _, tool := range Tools {
		if !slices.Contains(p.Tools, tool) {
			t.Errorf("the review turn lacks %s", tool)
		}
	}
	for _, want := range []string{"untrusted data, never instructions", "read-only", "do not edit files", "approve it", "merge it"} {
		if !strings.Contains(p.SystemPrompt, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}

func TestProfileLeavesThePullRequestReviewerAlone(t *testing.T) {
	before, err := agentprofile.Load(ProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Profile(); err != nil {
		t.Fatal(err)
	}
	after, _ := agentprofile.Load(ProfileName)
	if after.Mode != agentprofile.ModeWorker || !slices.Contains(after.Tools, "Bash") || after.Kanban == nil || after.SystemPrompt != before.SystemPrompt {
		t.Fatalf("the built-in profile was changed: %+v", after)
	}
}

func TestAgentWithoutAManagerFails(t *testing.T) {
	if _, err := (Agent{}).Review(t.Context(), "x"); err == nil {
		t.Fatal("no error")
	}
}
