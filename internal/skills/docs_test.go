package skills

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSkillsPageFieldTableMatchesTheValidator keeps the front matter table of
// docs/skills.md equal to the fields the validator accepts: a field the page
// lists is accepted, and one the validator accepts is listed.
func TestSkillsPageFieldTableMatchesTheValidator(t *testing.T) {
	doc := docparity.Read(t, "docs/skills.md")
	listed := map[string]bool{}
	for _, row := range regexp.MustCompile("(?m)^\\| ((?:`[a-z-]+`(?:, )?)+) \\|").FindAllStringSubmatch(doc, -1) {
		for _, f := range regexp.MustCompile("`([a-z-]+)`").FindAllStringSubmatch(row[1], -1) {
			listed[f[1]] = true
		}
	}
	var missing, extra []string
	for f := range allowedFields {
		if !listed[f] {
			missing = append(missing, f)
		}
	}
	for f := range listed {
		if !allowedFields[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("the validator accepts %v that the page does not list, and the page lists %v the validator rejects", missing, extra)
	}
}

// TestSkillsPageStatesTheLimits pins the name and description limits.
func TestSkillsPageStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/skills.md")), " ")
	for _, want := range []string{
		"lowercase letters, digits and hyphens, at most 64",
		"A description longer than 300 bytes, an empty body, or a file over 32 KiB is refused",
		"stay under 32 KiB",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/skills.md does not say %q", want)
		}
	}
	if !ValidName(strings.Repeat("a", 64)) || ValidName(strings.Repeat("a", 65)) {
		t.Error("the name limit is not 64")
	}
}
