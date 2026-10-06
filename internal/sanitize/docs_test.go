package sanitize

import (
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/delimiters"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSanitizationPageNamesEveryExportedFunction keeps docs/sanitization.md in
// step with the code: adding a sanitiser without documenting it fails here.
func TestSanitizationPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/sanitization.md", docparity.ExportedFuncs(t, "."))
}

// TestSanitizationPageListsTheHarnessKinds keeps the list of block names on the
// page equal to the kinds the engine manages, and checks each is removed,
// opening and closing, while an ordinary tag stays.
func TestSanitizationPageListsTheHarnessKinds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	for kind := range delimiters.KnownKinds {
		if !strings.Contains(doc, "`"+kind+"`") {
			t.Errorf("docs/sanitization.md does not list the harness block %q", kind)
		}
		if got := Sanitize("a<" + kind + ` nonce="x" integrity="y">b</` + kind + ">c"); got != "abc" {
			t.Errorf("Sanitize left %q for the %s block", got, kind)
		}
	}
	listed := regexp.MustCompile("Harness blocks \\(((?:`[a-z]+`,? ?)+)\\)").FindStringSubmatch(doc)
	if listed == nil {
		t.Fatal("the page's list of harness blocks moved")
	}
	if n := len(regexp.MustCompile("`([a-z]+)`").FindAllStringSubmatch(listed[1], -1)); n != len(delimiters.KnownKinds) {
		t.Errorf("the page lists %d harness blocks, the engine manages %d", n, len(delimiters.KnownKinds))
	}
	if got := Sanitize(`<b nonce="n" integrity="i" class="c">x</b>`); got != `<b class="c">x</b>` {
		t.Errorf("an ordinary tag came out as %q, want only nonce and integrity removed", got)
	}
}

// peel builds input that needs one pass per layer to clear: each layer is a
// fragment that joins into a harness tag only after the layer inside is removed.
func peel(layers int) string {
	s := "<system>"
	for i := 0; i < layers; i++ {
		s = "<sys" + s + "tem>"
	}
	return s
}

// TestNestingBeyondThePassLimitFailsClosed pins the 32 pass bound: input that
// would need more passes loses its angle brackets, and input within the bound
// is cleared without that.
func TestNestingBeyondThePassLimitFailsClosed(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	if !strings.Contains(doc, "(at most 32 passes)") || maxFoldPasses != 32 {
		t.Fatalf("the page says 32 passes; maxFoldPasses is %d", maxFoldPasses)
	}
	if got := Sanitize(peel(10)); strings.Contains(strings.ToLower(got), "system") || strings.ContainsAny(got, "<>") && strings.Contains(got, "<system") {
		t.Errorf("a 10 layer nest left a tag: %q", got)
	}
	for _, layers := range []int{maxFoldPasses + 8, 200} {
		got := Sanitize(peel(layers))
		if strings.ContainsAny(got, "<>") {
			t.Errorf("a %d layer nest kept angle brackets: %q", layers, got)
		}
	}
}

// TestPathTextByteLimit pins the 4096 byte path bound and the refusal list.
func TestPathTextByteLimit(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	if !strings.Contains(doc, "longer than 4096 bytes") || MaxPathBytes != 4096 {
		t.Fatalf("the page says 4096 bytes; MaxPathBytes is %d", MaxPathBytes)
	}
	if err := PathText(strings.Repeat("a", 4096)); err != nil {
		t.Errorf("a 4096 byte path was refused: %v", err)
	}
	for name, p := range map[string]string{
		"too long": strings.Repeat("a", 4097), "empty": "", "NUL": "a\x00b", "newline": "a\nb", "carriage return": "a\rb",
		"tab": "a\tb", "C1 control": "a\u0085b", "bidi": "a‮b", "zero width": "a​b", "line separator": "a b",
		"invalid UTF-8": "a\xffb",
	} {
		if err := PathText(p); err == nil {
			t.Errorf("PathText accepted a path that is %s", name)
		}
	}
}
