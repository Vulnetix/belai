package config

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestJevJobsPageDocumentsEveryJobAndKey keeps docs/jev-jobs.md in step with
// the shipped jobs and the settings keys.
func TestJevJobsPageDocumentsEveryJobAndKey(t *testing.T) {
	doc := docparity.Read(t, "docs/jev-jobs.md")
	for _, j := range JevJobs {
		if !strings.Contains(doc, "`"+string(j)+"`") {
			t.Errorf("docs/jev-jobs.md does not document the job %q", j)
		}
	}
	for _, key := range []string{"jev.jobs.", "jev.locate_previews", "jev.thresholds.", "deny_at", "allow_at", LocatePreviewsLocal, LocatePreviewsHosted, LocatePreviewsOff} {
		if !strings.Contains(doc, key) {
			t.Errorf("docs/jev-jobs.md does not mention %q", key)
		}
	}
}

// TestJevSettingsAreInTheReadme keeps the README settings table honest.
func TestJevSettingsAreInTheReadme(t *testing.T) {
	readme := docparity.Read(t, "README.md")
	for _, key := range []string{"`jev`", "docs/jev-jobs.md"} {
		if !strings.Contains(readme, key) {
			t.Errorf("README.md does not mention %s", key)
		}
	}
}
