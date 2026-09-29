package shellsafe

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSanitizationPageNamesEveryExportedFunction keeps docs/sanitization.md in
// step with the code.
func TestSanitizationPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/sanitization.md", docparity.ExportedFuncs(t, "."))
}

// TestSanitizationPageNamesTheReadOnlyPolicy pins the documented flag policy to
// the tables in code: every denied flag and wrapper is on the page.
func TestSanitizationPageNamesTheReadOnlyPolicy(t *testing.T) {
	doc := docparity.Read(t, "docs/sanitization.md")
	for name, d := range deniedFlags {
		if strings.Fields(d.long) == nil && d.short == "" {
			continue
		}
		if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, name) {
			t.Errorf("docs/sanitization.md does not mention the flag policy for %s", name)
		}
		for _, l := range strings.Fields(d.long) {
			if !strings.Contains(doc, l) {
				t.Errorf("docs/sanitization.md does not mention %s --%s", name, l)
			}
		}
	}
	for w := range simpleWrappers {
		if !strings.Contains(doc, w) {
			t.Errorf("docs/sanitization.md does not mention the wrapper %s", w)
		}
	}
	for _, g := range gitDeniedGlobal {
		if !strings.Contains(doc, "`"+g+"`") {
			t.Errorf("docs/sanitization.md does not mention git option %s", g)
		}
	}
	for f := range findUnsafe {
		if !strings.Contains(doc, "`"+f+"`") {
			t.Errorf("docs/sanitization.md does not mention find primary %s", f)
		}
	}
	for d := range trustedDirs {
		if !strings.Contains(doc, d) {
			t.Errorf("docs/sanitization.md does not mention the trusted directory %s", d)
		}
	}
}
