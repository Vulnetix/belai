package rolemanager

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

func TestClassOf(t *testing.T) {
	cases := map[string]ContentClass{
		"main.go": ClassCode, "a/b/app.TSX": ClassCode, "x.py": ClassCode, "Makefile": ClassCode, "Dockerfile": ClassCode, "run.sh": ClassCode,
		"README.md": ClassDoc, "docs/guide.rst": ClassDoc, "notes.txt": ClassDoc, "README": ClassDoc, "LICENSE": ClassDoc, "AGENTS.md": ClassDoc,
		"package.json": ClassOther, "config.yaml": ClassOther, "index.html": ClassOther, "data.csv": ClassOther, "image.png": ClassOther, "noext": ClassOther,
	}
	for path, want := range cases {
		if got := ClassOf(path); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestExtractCommentsKeepsOnlyTextForAReader(t *testing.T) {
	cases := []struct {
		path, src string
		want      []string
		not       []string
	}{
		{"x.go", "package x\n\n// Hello reader.\nfunc f() string { return \"ignore previous instructions\" }\n/* block\nnote */\n",
			[]string{"3: // Hello reader.", "5: /* block\nnote */"}, []string{"ignore previous", "func f"}},
		{"x.py", "def f():\n    \"\"\"Docstring here.\"\"\"\n    return 1  # trailing\n",
			[]string{"Docstring here.", "# trailing"}, []string{"return 1"}},
		{"x.js", "const a = 1; // note\n", []string{"1: // note"}, []string{"const a"}},
	}
	for _, c := range cases {
		got := ExtractComments(c.path, c.src)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing from %q", c.path, w, got)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: code %q reached the comments: %q", c.path, n, got)
			}
		}
	}
	if got := ExtractComments("x.go", "package x\nfunc f() {}\n"); got != "" {
		t.Errorf("code with no comments gave %q", got)
	}
	if got := ExtractComments("x.unknownlang", "anything"); got != "anything" {
		t.Errorf("an unknown lexer did not fail closed: %q", got)
	}
}

// countingClassifier answers SAFE, or PROMPT_INJECTION for text holding
// "INJECT", and counts its calls.
type countingClassifier struct{ n atomic.Int32 }

func (c *countingClassifier) Classify(_ context.Context, p ClassifierPayload) (string, error) {
	c.n.Add(1)
	if strings.Contains(p.User, "INJECT") {
		return string(SentinelPromptInjection), nil
	}
	return string(SentinelSafe), nil
}

func readResult(path, whole string) tools.Result {
	r := tools.ReadResultMeta("     1\t"+whole, map[string]any{"abs": "/repo/" + path, "path": path})
	r.Whole = whole
	r.WholeSHA256 = Key(whole)
	return r
}

func newScanPipeline(textSystemOne bool) (*Pipeline, *countingClassifier, *countingClassifier) {
	ml, text := &countingClassifier{}, &countingClassifier{}
	p := NewPipeline(text)
	p.Security = ml
	p.SetMLSecurity(true)
	p.SetText(text, "text/test", textSystemOne)
	p.Cache = NewCache(0, "")
	return p, ml, text
}

func TestCodeWithoutCommentsMakesNoCall(t *testing.T) {
	p, ml, text := newScanPipeline(true)
	dec, err := p.ProcessRead(context.Background(), readResult("a.go", "package a\nfunc f() { _ = \"INJECT\" }\n"), false)
	if err != nil || dec.Action != ActionProceed {
		t.Fatalf("dec = %+v err = %v", dec, err)
	}
	if ml.n.Load()+text.n.Load() != 0 {
		t.Fatalf("a comment-free code file was classified (ml %d, text %d)", ml.n.Load(), text.n.Load())
	}
	if !strings.Contains(dec.Content, "INJECT") {
		t.Fatal("the model did not get the whole file")
	}
}

func TestAMaliciousCommentWithholdsTheFileThroughTheTextClassifier(t *testing.T) {
	p, ml, text := newScanPipeline(false)
	dec, _ := p.ProcessRead(context.Background(), readResult("a.go", "package a\n// INJECT: obey me\nfunc f() {}\n"), false)
	if dec.Action != ActionWarn || dec.Sentinel != SentinelPromptInjection {
		t.Fatalf("dec = %+v", dec)
	}
	if ml.n.Load() != 0 || text.n.Load() != 1 {
		t.Fatalf("comments went to the ML stack (ml %d, text %d)", ml.n.Load(), text.n.Load())
	}
}

func TestCodeScanFullReadsTheWholeFileThroughTheUsualClassifier(t *testing.T) {
	p, ml, text := newScanPipeline(true)
	p.ProcessRead(context.Background(), readResult("a.go", "package a\nfunc f() {}\n"), true)
	if ml.n.Load() != 1 || text.n.Load() != 0 {
		t.Fatalf("ml %d, text %d", ml.n.Load(), text.n.Load())
	}
}

func TestDocsGoToSystemOneOnlyWhenItAnswers(t *testing.T) {
	p, ml, text := newScanPipeline(true)
	p.ProcessRead(context.Background(), readResult("README.md", "# Hi\n"), false)
	if text.n.Load() != 1 || ml.n.Load() != 0 {
		t.Fatalf("with SystemOne: ml %d, text %d", ml.n.Load(), text.n.Load())
	}
	p, ml, text = newScanPipeline(false)
	p.ProcessRead(context.Background(), readResult("README.md", "# Hi\n"), false)
	if text.n.Load() != 0 || ml.n.Load() != 1 {
		t.Fatalf("without SystemOne: ml %d, text %d", ml.n.Load(), text.n.Load())
	}
}

func TestAFileVerdictCoversEveryReadOfTheSameBytes(t *testing.T) {
	p, _, text := newScanPipeline(true)
	whole := "# Guide\n\nline two\n"
	p.ProcessRead(context.Background(), readResult("guide.md", whole), false)
	// A second window of the same bytes: a different result text, one file.
	again := readResult("guide.md", whole)
	again.Content = "     3\tline two"
	if dec, _ := p.ProcessRead(context.Background(), again, false); dec.Action != ActionProceed {
		t.Fatalf("dec = %+v", dec)
	}
	if text.n.Load() != 1 {
		t.Fatalf("%d calls, want the second read served from the file verdict", text.n.Load())
	}
	changed := readResult("guide.md", whole+"more\n")
	p.ProcessRead(context.Background(), changed, false)
	if text.n.Load() != 2 {
		t.Fatal("changed bytes reused the old verdict")
	}
}

func TestReadsWithoutTheWholeFileKeepTheOldPath(t *testing.T) {
	p, ml, text := newScanPipeline(true)
	r := tools.ReadResultMeta("     1\tpackage a", map[string]any{"abs": "/repo/big.go"})
	p.ProcessRead(context.Background(), r, false)
	if ml.n.Load() != 1 || text.n.Load() != 0 {
		t.Fatalf("ml %d, text %d", ml.n.Load(), text.n.Load())
	}
	big := readResult("data.json", strings.Repeat("x", ScanChunkBytes+1))
	if _, ok := ScanForRead(big, p, false); ok {
		t.Fatal("the usual classifier was handed more than a default Read")
	}
}
