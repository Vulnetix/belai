package agentprofile

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestGatesSettingsAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	for _, want := range []string{"`kanban.gates.require`", "`kanban.gates.verify`", "`off`, `record` or `enforce`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md lacks %q", want)
		}
	}
	for _, mode := range []string{VerifyOff, VerifyRecord, VerifyEnforce} {
		if !strings.Contains(doc, "`"+mode+"`") {
			t.Errorf("docs/agent-profiles.md does not name the %q verify mode", mode)
		}
	}
}

func TestReviewAndAutoAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	for _, want := range []string{
		"`kanban.gates.review`",
		"`review` needs `verify` to be `enforce`",
		"`review`, `backlog` or `auto`",
		"`auto`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md lacks %q", want)
		}
	}
	if ListAuto != "auto" {
		t.Errorf("ListAuto is %q but the docs say auto", ListAuto)
	}
}

func TestDraftIsDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	if !strings.Contains(doc, "`kanban.gates.draft`") || !strings.Contains(doc, "A drafted gate is always manual") {
		t.Error("docs/agent-profiles.md does not describe kanban.gates.draft")
	}
}
