package tools

import (
	"slices"
	"testing"
)

// The kinds whose content is arbitrary go to the classifier: an arbitrary
// command, a page written off this machine, a search result, a file whose
// bytes the harness did not write, and a remote CLI result carrying
// third-party repository text.
func TestArbitraryContentKindsClassify(t *testing.T) {
	for _, k := range []Kind{KindBash, KindWebFetch, KindWebSearch, KindRead, KindRemote, KindProcess, KindAgentStore, KindSubagent} {
		if !k.NeedsClassifier() {
			t.Errorf("%q result skipped the classifier", k)
		}
	}
}

// The shaped, controlled kinds do not: Grep returns matching lines for a
// pattern the harness passed as one argument, Glob returns paths, Write and
// Edit return a confirmation the harness wrote itself, and a local native
// runs a fixed argv the harness built.
func TestShapedKindsDoNotClassify(t *testing.T) {
	for _, k := range []Kind{KindGrep, KindGlob, KindWrite, KindEdit, KindNative, KindProcessCtl} {
		if k.NeedsClassifier() {
			t.Errorf("%q result asked for the classifier", k)
		}
	}
}

// An unregistered kind is sanitise-only rather than a panic. That is the
// cheap default, which is why adding a tool whose content is arbitrary has to
// be a deliberate edit to classifierKinds.
func TestUnknownKindDoesNotClassify(t *testing.T) {
	if Kind("nonsense").NeedsClassifier() {
		t.Fatal("an unknown kind asked for the classifier")
	}
}

// Every kind in the set must be a registered kind, so a rename cannot leave a
// dead entry silently exempting the real one.
func TestClassifierKindsAreRegisteredKinds(t *testing.T) {
	for k := range classifierKinds {
		if !slices.Contains(AllKinds, k) {
			t.Errorf("classifierKinds contains %q, which is not in AllKinds", k)
		}
	}
}

// The set is exactly the arbitrary-content kinds. Pinning it whole means a
// new kind cannot be added to either side without this test being updated on
// purpose.
func TestClassifierKindsIsExactlyTheArbitraryContentSet(t *testing.T) {
	want := map[Kind]bool{
		KindBash:       true,
		KindWebFetch:   true,
		KindWebSearch:  true,
		KindRead:       true,
		KindRemote:     true,
		KindProcess:    true,
		KindProcessCtl: false,
		KindAgentStore: true,
		KindSubagent:   true,
	}
	for _, k := range AllKinds {
		if got := k.NeedsClassifier(); got != want[k] {
			t.Errorf("Kind(%q).NeedsClassifier() = %v, want %v", k, got, want[k])
		}
	}
}
