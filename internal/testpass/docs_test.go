package testpass

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/testrun"
)

const testingDoc = "docs/testing.md"

// TestTestingPageNamesEveryExportedFunction keeps docs/testing.md in step with
// the code of the four packages the feature spans.
func TestTestingPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, testingDoc, docparity.ExportedFuncs(t, "."))
	docparity.RequireMentions(t, testingDoc, docparity.ExportedFuncs(t, "../testdetect"))
	docparity.RequireMentions(t, testingDoc, docparity.ExportedFuncs(t, "../testrun"))
	for _, name := range []string{"DecideTestReport", "ComposeTestReport", "BuildTestReportPayload", "CleanTestReport"} {
		docparity.RequireMentions(t, testingDoc, []string{name})
	}
}

// TestTestingPageNamesEverySettingKey keeps the settings table complete.
func TestTestingPageNamesEverySettingKey(t *testing.T) {
	doc := docparity.Read(t, testingDoc)
	for _, key := range []string{
		"tests.post_end", "tests.command", "tests.scope", "tests.on_fail",
		"tests.max_fix_passes", "tests.timeout_seconds", "tests.report",
	} {
		if !strings.Contains(doc, key) {
			t.Errorf("%s does not document %s", testingDoc, key)
		}
	}
}

// TestTestingPageNamesEveryStatusAndTrigger keeps the result vocabulary and
// the trigger table in step with the constants.
func TestTestingPageNamesEveryStatusAndTrigger(t *testing.T) {
	doc := docparity.Read(t, testingDoc)
	for _, s := range []testrun.Status{
		testrun.Passed, testrun.Failed, testrun.TimedOut,
		testrun.Denied, testrun.NeedsApproval, testrun.Errored,
	} {
		if !strings.Contains(doc, "`"+string(s)+"`") {
			t.Errorf("%s does not name the %q status", testingDoc, s)
		}
	}
	for _, level := range []string{"off", "goal", "goal_plan", "session"} {
		if !strings.Contains(doc, "`"+level+"`") {
			t.Errorf("%s does not name the post_end level %q", testingDoc, level)
		}
	}
}

// TestSiteAndReadmeMentionThePass keeps the user-facing pages aligned.
func TestSiteAndReadmeMentionThePass(t *testing.T) {
	if !strings.Contains(docparity.Read(t, "README.md"), "docs/testing.md") {
		t.Error("README.md does not link docs/testing.md")
	}
	if !strings.Contains(docparity.Read(t, "docs/README.md"), "testing.md") {
		t.Error("docs/README.md does not index testing.md")
	}
	if !strings.Contains(docparity.Read(t, "site/src/components/sections/Qol.astro"), "post-end test pass") {
		t.Error("the site does not describe the post-end test pass")
	}
}
