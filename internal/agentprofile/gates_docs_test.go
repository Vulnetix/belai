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
