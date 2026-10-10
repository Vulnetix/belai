package plans

import (
	"strings"
	"testing"
)

func TestParseDocRoundTrip(t *testing.T) {
	md := `# Refactor the parser

## Summary

Split the parser into lexer and grammar stages.

## Key Changes

1. Extract a tokeniser module
   - Files: parser/lex.go, parser/lex_test.go
   - Verify: go test ./parser
2. Rewrite the grammar in terms of tokens
   - Files: parser/grammar.go

## Test Plan

- go test ./...
- go vet ./...

## Assumptions

- The token vocabulary is stable.

## Risks

- The grammar rewrite may change error messages.
`
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if d.Title != "Refactor the parser" {
		t.Fatalf("Title = %q", d.Title)
	}
	if !strings.Contains(d.Summary, "lexer and grammar") {
		t.Fatalf("Summary = %q", d.Summary)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(d.Steps))
	}
	if d.Steps[0].N != 1 || d.Steps[0].Text != "Extract a tokeniser module" {
		t.Fatalf("step 0 = %+v", d.Steps[0])
	}
	if len(d.Steps[0].Files) != 2 || d.Steps[0].Files[0] != "parser/lex.go" {
		t.Fatalf("step 0 files = %v", d.Steps[0].Files)
	}
	if d.Steps[0].Verify != "go test ./parser" {
		t.Fatalf("step 0 verify = %q", d.Steps[0].Verify)
	}
	if len(d.Tests) != 2 || len(d.Assumptions) != 1 || len(d.Risks) != 1 {
		t.Fatalf("sections wrong: tests=%v assumptions=%v risks=%v", d.Tests, d.Assumptions, d.Risks)
	}

	rendered := d.Render()
	for _, want := range []string{"# Refactor the parser", "## Summary", "## Steps", "1. Extract a tokeniser module", "- Files: parser/lex.go", "- Verify: go test ./parser", "## Test Plan", "## Assumptions", "## Risks"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Render missing %q:\n%s", want, rendered)
		}
	}
	// The canonical render re-parses to the same structure.
	d2, err := ParseDoc(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered: %v", err)
	}
	if len(d2.Steps) != 2 || d2.Steps[0].Text != d.Steps[0].Text {
		t.Fatalf("round trip changed steps: %+v", d2.Steps)
	}
}

// TestParseDocKeepsStepDetail covers what models actually write under a step:
// the substance as plain sub-bullets, Files and Verify in several spellings,
// and a bolded field name. Every line stays with its step through a render,
// instead of being dropped to the end of the document.
func TestParseDocKeepsStepDetail(t *testing.T) {
	md := `## Summary

Add PATCH.

## Key Changes

1. **Store: add Patch method** (store/store.go)
   - Signature: Patch(id int, title *string) (Note, bool)
   - Lock the store while mutating.
   - Files touched: store/store.go
   - Verification: go test ./store
2. Route PATCH in the handler
   - **Files:** main.go
   - Verify with: curl -X PATCH localhost:8080/notes/1
   - Verify: go vet ./...

## Verification

- go test ./...

## Decisions

- null means absent.
`
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(d.Steps))
	}
	s0 := d.Steps[0]
	if len(s0.Notes) != 2 || !strings.Contains(s0.Notes[0], "Signature") || !strings.Contains(s0.Notes[1], "Lock the store") {
		t.Fatalf("step 0 notes = %q", s0.Notes)
	}
	if len(s0.Files) != 1 || s0.Files[0] != "store/store.go" {
		t.Fatalf("step 0 files = %v", s0.Files)
	}
	if s0.Verify != "go test ./store" {
		t.Fatalf("step 0 verify = %q", s0.Verify)
	}
	s1 := d.Steps[1]
	if len(s1.Files) != 1 || s1.Files[0] != "main.go" {
		t.Fatalf("step 1 files = %v (bold field name)", s1.Files)
	}
	if s1.Verify != "curl -X PATCH localhost:8080/notes/1; go vet ./..." {
		t.Fatalf("step 1 verify = %q", s1.Verify)
	}
	if len(d.Tests) != 1 || len(d.Assumptions) != 1 {
		t.Fatalf("section aliases: tests=%v assumptions=%v", d.Tests, d.Assumptions)
	}
	if d.Extra != "" {
		t.Fatalf("Extra = %q, want nothing detached from its step", d.Extra)
	}

	rendered := d.Render()
	// The note lines render between their step and the next one.
	i0 := strings.Index(rendered, "1. **Store")
	iSig := strings.Index(rendered, "Signature")
	i1 := strings.Index(rendered, "2. Route")
	if !(i0 < iSig && iSig < i1) {
		t.Fatalf("notes not rendered under their step:\n%s", rendered)
	}
	d2, err := ParseDoc(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered: %v", err)
	}
	if len(d2.Steps) != 2 || len(d2.Steps[0].Notes) != 2 || d2.Steps[0].Verify != s0.Verify || len(d2.Steps[1].Files) != 1 {
		t.Fatalf("round trip changed steps: %+v", d2.Steps)
	}
}

func TestParseDocRejectsZeroSteps(t *testing.T) {
	_, err := ParseDoc("# Title\n\n## Summary\n\nNo steps here.\n")
	if err == nil {
		t.Fatal("expected error for a plan with no numbered steps")
	}
}

func TestParseDocSkipsFencedCode(t *testing.T) {
	md := `# Plan

## Steps

1. Do the real thing

` + "```" + `
1. not a step
2. also not a step
` + "```" + `
`
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if len(d.Steps) != 1 {
		t.Fatalf("Steps = %d, want 1 (fenced numbered lines must not count)", len(d.Steps))
	}
}

func TestParseDocPreservesExtra(t *testing.T) {
	md := `# Plan

## Steps

1. Step one

## Deployment

Deploy with make release.
`
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if !strings.Contains(d.Extra, "## Deployment") || !strings.Contains(d.Extra, "make release") {
		t.Fatalf("Extra = %q, want the deployment section preserved", d.Extra)
	}
}

// Models write Files as an empty line with the paths nested under it as a
// list. Those paths belong to the step's Files, not to its loose notes, and
// the lint must not call such a step file-less.
func TestParseDocReadsNestedFilesAndVerifyLists(t *testing.T) {
	md := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Add the engine\n   - Files:\n     - `src/a.ts` (new)\n     - `test/a.test.ts` (new)\n   - Implement the thing.\n   - Verify:\n     - `npm test`\n2. Wire it\n   - Files: `src/b.ts`\n   - Verify: `npm run build`\n   - Verify: `npm run lint`\n"
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatal(err)
	}
	s0 := d.Steps[0]
	if len(s0.Files) != 2 || s0.Files[0] != "`src/a.ts` (new)" || s0.Files[1] != "`test/a.test.ts` (new)" {
		t.Fatalf("step 1 files = %q", s0.Files)
	}
	if s0.Verify != "`npm test`" {
		t.Fatalf("step 1 verify = %q", s0.Verify)
	}
	if len(s0.Notes) != 1 || !strings.Contains(s0.Notes[0], "Implement the thing") {
		t.Fatalf("step 1 notes = %q: the nested paths must not stay behind as notes", s0.Notes)
	}
	if got := d.Steps[1].Verify; got != "`npm run build`; `npm run lint`" {
		t.Fatalf("two Verify lines should both be kept: %q", got)
	}
	// Two Verify lines are two commands; the lint says so. A one-command variant is clean.
	if issues := Lint(strings.Replace(md, "   - Verify: `npm run lint`\n", "", 1)); len(issues) != 0 {
		t.Fatalf("a well-formed plan with nested lists was flagged: %v", issues)
	}
	// The canonical render re-parses to the same Files.
	d2, err := ParseDoc(d.Render())
	if err != nil || len(d2.Steps[0].Files) != 2 {
		t.Fatalf("round trip lost the files: %v %+v", err, d2.Steps)
	}
}

// A nested Files bullet is one entry. Models write "path (new): what it does,
// with commas"; splitting it on commas turned one file into a dozen fragments
// (a measured run's plan listed "RateLimitConfig" and "nowMs" as files).
func TestParseDocKeepsANestedFilesBulletWhole(t *testing.T) {
	md := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Add the limiter\n   - Files:\n     - `src/rate-limit.ts` (new): export `RateLimitGroup`, `RateLimitConfig`; implement `check(subject, group, nowMs)`\n     - `test/rate-limit.test.ts` (new): assert allow, block, reset\n   - Verify: `npm test`\n"
	d, err := ParseDoc(md)
	if err != nil {
		t.Fatal(err)
	}
	files := d.Steps[0].Files
	if len(files) != 2 || !strings.HasPrefix(files[0], "`src/rate-limit.ts` (new): export") || !strings.Contains(files[0], "nowMs)") {
		t.Fatalf("files = %q, want the two bullets whole", files)
	}
	if issues := Lint(md); len(issues) != 0 {
		t.Fatalf("lint = %v", issues)
	}
}
