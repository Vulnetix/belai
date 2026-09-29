package tools

import (
	"context"
	"fmt"
	"strings"
)

// Locate ranks the files a question is about. It is offered only while the
// explore_locate job is on (a decision backend is configured); it reads no
// file into the conversation. The result is paths, line numbers and scores the
// harness computed, so it is shaped output like Glob and is sanitised, not
// classified. Declared names and file text never appear in it.

// LocateName is the tool's name.
const LocateName = "Locate"

// LocateHit is one ranked file.
type LocateHit struct {
	Path string
	// Line is the declaration that best matches the question, or 0.
	Line int
	// Percent is the score as a whole percent.
	Percent int
	// Lead marks a weaker match, worth reading only if the strong ones fall short.
	Lead bool
}

// LocateResult is what a search found.
type LocateResult struct {
	Hits []LocateHit
	// By names who ranked: a short model id, or "keywords" when the backend
	// did not answer.
	By string
}

// Locator ranks files for a question.
type Locator interface {
	Locate(ctx context.Context, question string, max int) LocateResult
}

// Locate is the tool.
type Locate struct {
	Locator Locator
}

// Definition describes the tool.
func (Locate) Definition() Definition {
	return Definition{
		Name: LocateName,
		Description: "Rank the files in the working directory that a question is most likely about, before you read any. " +
			"Returns paths with a line number and a score; it does not return file contents. Use it to decide which files to Read or Grep. " +
			"Files that are ignored, hidden, binary, or hold credentials are never listed.",
		Properties: map[string]Property{
			"query":       {Type: "string", Description: "What you are looking for, in words: a feature, a symbol, a behaviour."},
			"max_results": {Type: "integer", Description: "How many files to return at most (default 12, at most 25)."},
		},
		Required: []string{"query"},
	}
}

// Kind is glob: a list of paths the harness composed.
func (Locate) Kind() Kind { return KindGlob }

// Subject has no permission subject.
func (Locate) Subject(map[string]any) string { return "" }

// Mutates reports false.
func (Locate) Mutates() bool { return false }

// Execute runs the search.
func (t Locate) Execute(ctx context.Context, args map[string]any) (Result, error) {
	q, _ := argString(args, "query")
	q = strings.TrimSpace(q)
	if q == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	if t.Locator == nil {
		return Result{Kind: KindGlob, Content: "locate is not available in this session"}, nil
	}
	max := 12
	if n, ok := argInt64(args, "max_results"); ok && n > 0 {
		max = int(min(n, 25))
	}
	res := t.Locator.Locate(ctx, q, max)
	if len(res.Hits) == 0 {
		return Result{Kind: KindGlob, Content: "no file matches the question"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) ranked by %s:\n", len(res.Hits), res.By)
	for i, h := range res.Hits {
		loc := h.Path
		if h.Line > 0 {
			loc = fmt.Sprintf("%s:%d", h.Path, h.Line)
		}
		note := ""
		if h.Lead {
			note = " (lead)"
		}
		fmt.Fprintf(&b, "%d. %s  %d%%%s\n", i+1, loc, h.Percent, note)
	}
	return Result{Kind: KindGlob, Content: strings.TrimRight(b.String(), "\n")}, nil
}

var (
	_ Tool    = Locate{}
	_ Mutator = Locate{}
)
