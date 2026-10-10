package tools

import (
	"context"
	"strings"
	"testing"
)

type fakeCloser struct {
	n, keep int
	reason  string
}

func (f *fakeCloser) CloseDuplicatePR(_ context.Context, n, keep int, reason string) (string, error) {
	f.n, f.keep, f.reason = n, keep, reason
	return "closed", nil
}

func TestCloseDuplicatePRNeedsBothNumbersAndAReason(t *testing.T) {
	c := &fakeCloser{}
	tool := CloseDuplicatePR{C: c, Open: "#32; #33"}
	for _, args := range []map[string]any{
		{"keep": 32, "reason": "r"},
		{"close": 33, "reason": "r"},
		{"close": 33, "keep": 32},
		{"close": -1, "keep": 32, "reason": "r"},
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	res, err := tool.Execute(context.Background(), map[string]any{"close": float64(33), "keep": "32", "reason": "  the  README\ntable is complete  "})
	if err != nil || res.Content != "closed" || c.n != 33 || c.keep != 32 || c.reason != "the README table is complete" {
		t.Fatalf("%+v %v %+v", res, err, c)
	}
	if !strings.Contains(tool.Definition().Description, "#32; #33") || tool.Subject(map[string]any{"close": 33}) != "#33" {
		t.Fatal("definition or subject")
	}
	if _, err := (CloseDuplicatePR{}).Execute(context.Background(), map[string]any{"close": 1, "keep": 2, "reason": "r"}); err == nil {
		t.Fatal("ran without a closer")
	}
}
