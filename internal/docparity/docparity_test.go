package docparity

import (
	"slices"
	"testing"
)

func TestExportedFuncsFindsFunctionsAndMethods(t *testing.T) {
	got := ExportedFuncs(t, ".")
	for _, want := range []string{"ExportedFuncs", "Read", "RequireMentions"} {
		if !slices.Contains(got, want) {
			t.Errorf("ExportedFuncs missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "receiverName") {
		t.Error("unexported function listed")
	}
}

func TestReadFindsRepositoryFiles(t *testing.T) {
	if doc := Read(t, "docs/sanitization.md"); len(doc) == 0 {
		t.Fatal("empty document")
	}
}

func TestReceiverName(t *testing.T) {
	if receiverName(nil) != "" {
		t.Fatal("nil receiver must have no name")
	}
}
