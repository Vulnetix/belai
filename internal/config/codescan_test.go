package config

import "testing"

// A repository may make the classifier read whole code files, never less of
// them than the user chose.
func TestProjectCodeScanOnlyTightens(t *testing.T) {
	global := Settings{Classifier: &ClassifierSettings{CodeScan: CodeScanFull}}
	if got := global.Override(Settings{Classifier: &ClassifierSettings{CodeScan: CodeScanComments}}); !got.ClassifierCodeScanFull() {
		t.Fatal("a project loosened code_scan to comments")
	}
	if got := (Settings{}).Override(Settings{Classifier: &ClassifierSettings{CodeScan: CodeScanFull}}); !got.ClassifierCodeScanFull() {
		t.Fatal("a project could not tighten code_scan to full")
	}
	if (Settings{}).ClassifierCodeScanFull() {
		t.Fatal("comments is not the default")
	}
}
