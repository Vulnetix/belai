package mergefix

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/merge-conflicts.md is the contract the website parses the ledger by.
// These tests fail when the note format, a result, a choice or a final note
// changes without the page following.
func TestTheLedgerFormatIsDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/merge-conflicts.md")
	for _, want := range []string{
		"conflict attempt N/5 <fingerprint8>: <file>=<both|current|incoming|mixed|other>[, ...] -> <passed|build failed (<command>)|lint failed (<linter>: <count>)|tests failed (<suite>)|markers left|duplicate imports (<file>)|repeat of attempt M>",
		"At most 12 files are listed, then `+K more`",
		"`conflicts resolved on attempt N, pushed <sha12>`",
		"`conflicts resolved on attempt N, not pushed: <reason>`",
		"`conflicts unresolved after 5 attempts`",
		"`checkpoint:conflicts-unresolved`",
		"`" + RetryNote(0)[:len("conflict retry: round ")] + "R`",
		"## Retrying an unresolved card",
		"attempts are numbered from 1 again",
		"first eight hex characters of the sha256 of the sorted",
		"no `+` or\n  `--force`",
		"| Pushed to a same-repository pull request | the pull request's head, `<head>` |",
		"| A fork, its resolution published to `origin` | the attempt's branch, `belai/K-xxxxxx/aN` |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/merge-conflicts.md lacks %q", want)
		}
	}
	for _, c := range append(append([]Choice(nil), Uniform...), Mixed, Other) {
		if !strings.Contains(doc, "| `"+string(c)+"` |") {
			t.Errorf("the choice table has no row for %s", c)
		}
	}
	for _, o := range []Result{{Outcome: MarkersFound}, {Outcome: DuplicateImportsFound, File: "<file>"}, {Outcome: Passed}} {
		s := o.String()
		if o.Outcome == DuplicateImportsFound {
			s = "duplicate imports"
		}
		if !strings.Contains(doc, s) {
			t.Errorf("docs/merge-conflicts.md does not name the result %q", s)
		}
	}
	if FinalUnresolved(5) != "conflicts unresolved after 5 attempts" || !strings.HasPrefix(FinalResolved(1, "x"), "conflicts resolved on attempt 1, pushed ") {
		t.Error("the final notes changed")
	}
	kanban := docparity.Read(t, "docs/kanban.md")
	for _, want := range []string{"`merge-conflict`", "`conflicts-unresolved`", "merge-conflicts.md"} {
		if !strings.Contains(kanban, want) {
			t.Errorf("docs/kanban.md lacks %q", want)
		}
	}
}
