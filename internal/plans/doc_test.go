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
