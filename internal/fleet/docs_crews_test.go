package fleet

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/vex"
)

// jsonKeys returns the JSON keys of a struct's exported fields.
func jsonKeys(v any) []string {
	rt := reflect.TypeOf(v)
	var keys []string
	for i := 0; i < rt.NumField(); i++ {
		if tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	return keys
}

// Every key of the security and quality blocks, and of a crew, is in the docs.
func TestProfileDocNamesEveryCrewAndBlockKey(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	for _, k := range jsonKeys(agentprofile.SecuritySpec{}) {
		if !strings.Contains(doc, "`kanban.security."+k+"`") {
			t.Errorf("docs/agent-profiles.md does not document kanban.security.%s", k)
		}
	}
	for _, k := range jsonKeys(agentprofile.QualitySpec{}) {
		if !strings.Contains(doc, "`kanban.quality."+k+"`") {
			t.Errorf("docs/agent-profiles.md does not document kanban.quality.%s", k)
		}
	}
	fleetDoc := docparity.Read(t, "docs/fleet.md")
	for _, k := range jsonKeys(agentprofile.Crew{}) {
		if k == "one_per_repo" && !strings.Contains(fleetDoc, "\""+k+"\": true") {
			t.Errorf("docs/fleet.md does not explain the crew key %s", k)
		}
	}
}

// The security crew's verdicts, labels and numbers as the code has them.
func TestFleetDocDescribesTheSecurityCrewAsCoded(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	for _, v := range []kanban.Verdict{kanban.VerdictFixed, kanban.VerdictFalsePositive, kanban.VerdictNoFix, kanban.VerdictNeedsHuman, kanban.VerdictRejected} {
		if !strings.Contains(doc, "`"+string(v)+"`") {
			t.Errorf("docs/fleet.md does not name the %q verdict", v)
		}
	}
	for _, l := range []string{kanban.LabelVuln, kanban.LabelGone, kanban.LabelNeedsVerify, EnrichLabel, agentprofile.QualityLabel} {
		if !strings.Contains(doc, "`"+l+"`") {
			t.Errorf("docs/fleet.md does not name the %q label", l)
		}
	}
	for _, want := range []string{
		"`.vulnetix/belai/quality/<commit>.json`",
		fmt.Sprintf("under %d%%", int(quality.LowCoverage)),
		"at most 20 cards", // maxEnrichTargets
	} {
		if !strings.Contains(strings.ToLower(doc), strings.ToLower(want)) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	if maxEnrichTargets != 20 {
		t.Errorf("maxEnrichTargets = %d; docs/fleet.md says 20", maxEnrichTargets)
	}
	if quality.KeepRecords != 20 || !strings.Contains(docparity.Read(t, "docs/testing.md"), "last twenty") {
		t.Errorf("docs/testing.md must say the last %d records are kept", quality.KeepRecords)
	}
}

// Every seed category, for a repository with suites and for one without, is
// named in the docs table.
func TestFleetDocNamesEverySeedCategory(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	full := quality.Build("f6808a50403cccea6f4e73c7bbbcc11395b65e50", time.Unix(0, 0),
		[]testrun.Result{{Suite: "go", Command: []string{"go", "test", "./..."}, Status: testrun.Passed}}, nil, t.TempDir())
	none := quality.Build("f6808a50403cccea6f4e73c7bbbcc11395b65e50", time.Unix(0, 0), nil, nil, t.TempDir())
	seen := map[string]bool{}
	for _, rec := range []quality.Record{full, none} {
		for _, s := range quality.Seeds(rec) {
			if !strings.HasPrefix(s.Finding, quality.PrefixCategory) {
				continue
			}
			name := strings.SplitN(strings.TrimPrefix(s.Finding, quality.PrefixCategory), ":", 2)[0]
			seen[name] = true
		}
	}
	if len(seen) != 7 {
		t.Fatalf("categories %v: the docs table lists seven", seen)
	}
	for name := range seen {
		if !strings.Contains(doc, "| `"+name+"`:") {
			t.Errorf("docs/fleet.md has no row for the %q seed", name)
		}
	}
}

// The five OpenVEX justifications, the statuses and the directories are named
// where the docs promise them.
func TestVulnetixDocNamesTheVEXContract(t *testing.T) {
	doc := docparity.Read(t, "docs/vulnetix.md")
	for _, j := range vex.Justifications() {
		if !strings.Contains(doc, "`"+j+"`") {
			t.Errorf("docs/vulnetix.md does not name the %q justification", j)
		}
	}
	for _, s := range []string{"fixed", "not_affected", "affected", "under_investigation"} {
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("docs/vulnetix.md does not name the %q VEX status", s)
		}
	}
	if !strings.Contains(doc, ".vulnetix/vex/<finding>.openvex.json") || vex.Dir != ".vulnetix/vex" {
		t.Errorf("the VEX directory %q is not documented", vex.Dir)
	}
	if !strings.Contains(docparity.Read(t, "docs/testing.md"), "."+"vulnetix/belai/quality") || quality.Dir != ".vulnetix/belai/quality" {
		t.Errorf("the quality directory %q is not documented", quality.Dir)
	}
	if !strings.Contains(docparity.Read(t, "docs/agent-profiles.md"), "0 to "+fmt.Sprint(agentprofile.MaxRounds)) {
		t.Errorf("docs/agent-profiles.md does not state the rounds bound %d", agentprofile.MaxRounds)
	}
}

// docs/kanban.md and docs/bkan.md name the verdict tool and the harness-set
// finding fields.
func TestKanbanDocsNameTheSecurityFields(t *testing.T) {
	kb := docparity.Read(t, "docs/kanban.md")
	if !strings.Contains(kb, tools.KanbanVerdictName) && !strings.Contains(docparity.Read(t, "docs/fleet.md"), tools.KanbanVerdictName) {
		t.Errorf("no doc names the %s tool", tools.KanbanVerdictName)
	}
	for _, f := range []string{"Finding", "SeenRef", "Verdict", "VEX"} {
		if !strings.Contains(docparity.Read(t, "docs/bkan.md"), "`"+f+"`") {
			t.Errorf("docs/bkan.md does not name Item.%s", f)
		}
	}
}
