package docsuite

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

var numberWords = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}

// word spells a small number the way the prose does.
func word(n int) string {
	if n >= 0 && n < len(numberWords) {
		return numberWords[n]
	}
	panic("docsuite: no word for the number")
}

// requirePhrases fails for each phrase the page does not contain. Phrases are
// matched with line breaks folded to spaces, since the pages wrap their prose.
func requirePhrases(t *testing.T, page string, phrases ...string) {
	t.Helper()
	doc := strings.Join(strings.Fields(docparity.Read(t, page)), " ")
	for _, p := range phrases {
		if !strings.Contains(doc, strings.Join(strings.Fields(p), " ")) {
			t.Errorf("%s does not say %q", page, p)
		}
	}
}
