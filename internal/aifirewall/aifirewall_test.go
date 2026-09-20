package aifirewall

import (
	"sort"
	"testing"
)

func TestProvidersSortedAndMatchesSlugs(t *testing.T) {
	got := Providers()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Providers() not sorted: %v", got)
	}
	if len(got) != len(slugs) {
		t.Fatalf("len = %d, want %d", len(got), len(slugs))
	}
	want := make([]string, 0, len(slugs))
	for name := range slugs {
		want = append(want, name)
	}
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Providers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
