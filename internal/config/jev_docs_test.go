package config

import (
	"fmt"
	"regexp"
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

// TestJevJobsPageDefaultsMatchEveryThreshold reads the two threshold tables of
// docs/jev-jobs.md: every cut-off the code has is on a row, and each row's
// default equals the code's, in the order the keys are listed.
func TestJevJobsPageDefaultsMatchEveryThreshold(t *testing.T) {
	doc := docparity.Read(t, "docs/jev-jobs.md")
	section := regexp.MustCompile(`(?s)## Scores and thresholds(.*?)How a request is sent:`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the thresholds section moved")
	}
	var none *JevThresholdSettings
	seen := map[string]bool{}
	for _, line := range strings.Split(section[1], "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		var keys []string
		var defaults string
		switch {
		case len(cells) == 4 && !strings.HasPrefix(cells[0], "-") && cells[0] != "Constant":
			keys = regexp.MustCompile("`([a-z_]+)`").FindAllString(cells[1], -1)
			defaults = cells[2]
		case len(cells) == 3 && !strings.HasPrefix(cells[0], "-") && cells[0] != "Setting":
			keys = regexp.MustCompile("`([a-z_]+)`").FindAllString(cells[0], -1)
			defaults = cells[1]
		default:
			continue
		}
		if len(keys) == 0 {
			continue
		}
		values := strings.Split(defaults, ", ")
		if len(values) != len(keys) {
			t.Errorf("the row for %v has %d defaults %q", keys, len(values), defaults)
			continue
		}
		for i, k := range keys {
			k = strings.Trim(k, "`")
			seen[k] = true
			v, _, ok := none.Value(k)
			if !ok {
				t.Errorf("the page documents the threshold %q, which does not exist", k)
				continue
			}
			if want := fmt.Sprintf("%.2f", v); values[i] != want {
				t.Errorf("%s: the page says %s, the default is %s", k, values[i], want)
			}
		}
	}
	for _, k := range JevThresholdKeys() {
		if !seen[k] {
			t.Errorf("the thresholds tables do not list %s", k)
		}
	}
}
