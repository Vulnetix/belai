package locate

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestJevJobsPageStatesTheLocateConstants keeps the explore locate section of
// docs/jev-jobs.md equal to the constants and rule tables the code uses.
func TestJevJobsPageStatesTheLocateConstants(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/jev-jobs.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("Up to %d directories (%d for the local model)", DirsRemote, DirsLocal),
		fmt.Sprintf("the best %d (%d for the local model)", FilesRemote, FilesLocal),
		fmt.Sprintf("At most %d files are listed", MaxFiles/1000*1000),
		fmt.Sprintf("files over %d MiB", MaxFileBytes>>20),
		fmt.Sprintf("up to %d", DefaultMaxHits),
		fmt.Sprintf("at most %d files", LexicalOnlyHits),
		fmt.Sprintf("A file at or above %.2f is a hit, one from %.2f to %.2f is a lead", HitAt, LeadAt, HitAt),
		fmt.Sprintf("below %.2f is dropped", LeadAt),
	} {
		// 100,000 is written with a comma in the page.
		want = strings.ReplaceAll(want, "100000", "100,000")
		if !strings.Contains(doc, want) {
			t.Errorf("docs/jev-jobs.md does not state %q", want)
		}
	}
	if MaxFiles != 100_000 {
		t.Error("MaxFiles changed: update docs/jev-jobs.md and this test")
	}
	for name := range dependencyDirs {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("docs/jev-jobs.md does not list the dependency directory %q", name)
		}
	}
	var sens []string
	for name := range sensitiveNames {
		sens = append(sens, name)
	}
	sort.Strings(sens)
	for _, name := range sens {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("docs/jev-jobs.md does not list the sensitive file %q", name)
		}
	}
	for _, s := range sensitiveSuffixes[:4] {
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("docs/jev-jobs.md does not list the key suffix %q", s)
		}
	}
}
