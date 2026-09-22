package lsp

import (
	"sort"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func TestKnownLanguagesMatchConfig(t *testing.T) {
	got := LanguageIDs()
	want := append([]string{}, config.KnownLSPLanguages...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("len mismatch: lsp=%v config=%v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at %d: lsp=%q config=%q", i, got[i], want[i])
		}
	}
}
