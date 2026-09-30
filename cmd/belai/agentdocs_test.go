package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// Every `belai agent` command in the usage text has a row in docs/fleet.md, so
// a new command cannot ship undocumented.
func TestAgentCommandsAreInTheFleetDocs(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	re := regexp.MustCompile(`(?m)^  ([a-z]+)\b`)
	seen := 0
	for _, m := range re.FindAllStringSubmatch(agentUsage, -1) {
		cmd := m[1]
		seen++
		if !strings.Contains(doc, "belai agent "+cmd) {
			t.Errorf("docs/fleet.md does not document `belai agent %s`", cmd)
		}
	}
	if seen < 12 {
		t.Fatalf("found only %d commands in the usage text; the parser is out of date", seen)
	}
	for _, cmd := range []string{"pause", "resume"} {
		if !strings.Contains(agentUsage, "  "+cmd+" ") {
			t.Errorf("the usage text lacks %s", cmd)
		}
	}
}
