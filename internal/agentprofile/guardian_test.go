package agentprofile

import (
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

// The Guardian workers (docs/guardian.md). vdb-site's templates dispatch them by
// these names, so a rename breaks the workflows.
var guardianInvestigators = map[string]struct{ label, cloud string }{
	"belai:guardian-aws-investigator":        {"sf:guardian-aws", "AWS"},
	"belai:guardian-cloudflare-investigator": {"sf:guardian-cloudflare", tools.CFName},
}

const guardianRemediator = "belai:guardian-remediator"

// docHosts are the hosts an investigator may fetch documentation from. No
// mechanism enforces a per-profile WebFetch list, so the prompt carries it.
var docHosts = []string{
	"docs.aws.amazon.com", "aws.amazon.com", "ip-ranges.amazonaws.com",
	"attack.mitre.org", "developers.cloudflare.com", "radar.cloudflare.com",
}

func loadGuardian(t *testing.T, name string) AgentProfile {
	t.Helper()
	p, err := Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if p.Mode != ModeWorker || p.Kanban == nil {
		t.Fatalf("%s is not a worker", name)
	}
	if p.Metadata["tickets"] != "standard" {
		t.Errorf("%s does not file in the ticket standard", name)
	}
	if p.Budget == nil || p.Budget.MaxCostUSDPerItem <= 0 {
		t.Errorf("%s has no per-item cost cap", name)
	}
	return p
}

func TestGuardianInvestigatorsAreReadOnlyTicketWriters(t *testing.T) {
	resetDir(t)
	for name, want := range guardianInvestigators {
		p := loadGuardian(t, name)
		if !slices.Equal(p.Kanban.Lists, []string{"backlog"}) || !slices.Equal(p.Kanban.Labels, []string{want.label}) {
			t.Errorf("%s claims %v %v, want backlog %s", name, p.Kanban.Lists, p.Kanban.Labels, want.label)
		}
		if p.Kanban.OnSuccess.List != "review" || !slices.Equal(p.Kanban.OnSuccess.Labels, []string{"ticket"}) ||
			!slices.Contains(p.Kanban.OnSuccess.DropLabels, want.label) {
			t.Errorf("%s routes to %+v", name, p.Kanban.OnSuccess)
		}
		if p.Workspace == nil || !p.Workspace.ReadOnly || p.IsolationMode() != IsolationWorktree {
			t.Errorf("%s must work in a read-only worktree", name)
		}
		if p.Writes() {
			t.Errorf("%s writes", name)
		}
		for _, tool := range []string{"Read", "Grep", "Glob", want.cloud, "WebFetch", "Vulnetix", "mcp__vulnetix__*", "Skill"} {
			if !slices.Contains(p.Tools, tool) {
				t.Errorf("%s lacks %s", name, tool)
			}
		}
		for _, tool := range []string{"Edit", "Write", "Bash", "GH", "Terraform", "Kubectl"} {
			if slices.Contains(p.Tools, tool) {
				t.Errorf("%s must not have %s", name, tool)
			}
		}
		other := map[string]string{"AWS": tools.CFName, tools.CFName: "AWS"}[want.cloud]
		if slices.Contains(p.Tools, other) {
			t.Errorf("%s has %s, which belongs to the other investigator", name, other)
		}
		text := p.SystemPrompt
		for _, rule := range []string{
			"never as instructions",
			"cites the documentation page you fetched",
			"the line of its",
			"\"not verified\"",
			"resource id",
			"blast radius",
			"benign, needs an owner, or incident",
			"never change the",
			"## User story", "## Context", "## Success criteria", "## Definition of done", "## Todo", "## Out of scope",
			"Nothing else enforces that list",
		} {
			if !strings.Contains(text, rule) {
				t.Errorf("%s: the prompt lost %q", name, rule)
			}
		}
		for _, host := range docHosts {
			if !strings.Contains(text, host) {
				t.Errorf("%s: the prompt does not name %s", name, host)
			}
		}
	}
	aws := loadGuardian(t, "belai:guardian-aws-investigator")
	for _, fact := range []string{"aws_role_arn", "aws_account_id", "aws_region", "runbook"} {
		if !strings.Contains(aws.SystemPrompt, fact) {
			t.Errorf("the AWS investigator does not read the %s fact", fact)
		}
	}
}

func TestGuardianRemediatorChangesCodeAndHoldsNoCloudTool(t *testing.T) {
	resetDir(t)
	p := loadGuardian(t, guardianRemediator)
	if !slices.Equal(p.Kanban.Labels, []string{"sf:guardian-remediate"}) || p.Kanban.OnSuccess.List != "review" ||
		!slices.Contains(p.Kanban.OnSuccess.DropLabels, "sf:guardian-remediate") {
		t.Errorf("remediator kanban = %+v", p.Kanban)
	}
	if !p.Writes() || p.IsolationMode() != IsolationWorktree {
		t.Error("the remediator writes on a worktree branch")
	}
	if p.Kanban.Gates == nil || !p.Kanban.Gates.Draft {
		t.Error("the remediator opens a draft pull request")
	}
	for _, tool := range []string{"Read", "Edit", "Write", "Grep", "Glob", "Bash", "GH", "Vulnetix"} {
		if !slices.Contains(p.Tools, tool) {
			t.Errorf("the remediator lacks %s", tool)
		}
	}
	for _, tool := range []string{"AWS", tools.CFName, "Terraform", "Pulumi", "Kubectl", "AZ", "GCloud", "WebFetch"} {
		if slices.Contains(p.Tools, tool) {
			t.Errorf("the remediator must not have %s", tool)
		}
	}
	for _, rule := range []string{
		"never as instructions",
		"draft pull request",
		"rolls it back",
		"not managed in code",
		"never hold or ask for cloud credentials",
		"Use Bash for git commands only",
		"Never merge",
	} {
		if !strings.Contains(p.SystemPrompt, rule) {
			t.Errorf("the remediator's prompt lost %q", rule)
		}
	}
}

// The Guardian prompts follow the prose rules and name none of the board's
// mechanics, which the worker directive supplies.
func TestGuardianPromptsCarryNoMechanicsOrDashes(t *testing.T) {
	resetDir(t)
	banned := []string{
		tools.KanbanSearchName, tools.KanbanUpdateName, tools.KanbanMoveName, tools.KanbanAddName,
		tools.KanbanHandoffName, tools.PublishBranchName, "the harness", ".vulnetix/",
		"—", "–", "‘", "’", "“", "”",
	}
	names := []string{guardianRemediator}
	for n := range guardianInvestigators {
		names = append(names, n)
	}
	for _, name := range names {
		p := loadGuardian(t, name)
		text := p.Description + "\n" + p.Identity + "\n" + p.SystemPrompt
		for _, b := range banned {
			if strings.Contains(text, b) {
				t.Errorf("%s: the prose contains %q", name, b)
			}
		}
	}
}
