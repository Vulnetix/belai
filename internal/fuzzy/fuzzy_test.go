package fuzzy

import (
	"reflect"
	"testing"
)

func TestScoreMatchesSubsequence(t *testing.T) {
	cases := []struct {
		q, t string
		ok   bool
	}{
		{"", "anything", true},
		{"pmt", "prompts", true},
		{"PMT", "prompts", true},
		{"dir", "add-dir", true},
		{"a:dbg", "agent:belai:debug", true},
		{"tmp", "prompts", false},
		{"bogus", "prompts", false},
		{"x", "", false},
	}
	for _, c := range cases {
		if _, ok := Score(c.q, c.t); ok != c.ok {
			t.Errorf("Score(%q, %q) ok = %v, want %v", c.q, c.t, ok, c.ok)
		}
	}
}

func TestScoreOrdering(t *testing.T) {
	better := func(q, a, b string) {
		t.Helper()
		sa, _ := Score(q, a)
		sb, _ := Score(q, b)
		if sa <= sb {
			t.Errorf("Score(%q): %q = %d, want above %q = %d", q, a, sa, b, sb)
		}
	}
	better("pro", "prompts", "permissions-override") // prefix beats scattered
	better("dir", "add-dir", "addxdxixr")            // boundary run beats gaps
	better("del", "model", "dxexl")                  // consecutive run beats scattered
}

func TestRankKeepsInputOrderOnTies(t *testing.T) {
	got := Strings("p", []string{"permissions", "process", "profile"})
	want := []string{"permissions", "process", "profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Strings(p) = %v, want %v", got, want)
	}
	if got := Strings("", []string{"b", "a"}); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("empty query reordered: %v", got)
	}
}

func TestRankBestKeyAndNoMatch(t *testing.T) {
	type item struct{ full, name string }
	items := []item{{"prompt:deploy", "deploy"}, {"agent:writer", "writer"}}
	got := Rank("dpl", items, func(i item) []string { return []string{i.full, i.name} })
	if len(got) != 1 || got[0].name != "deploy" {
		t.Fatalf("Rank(dpl) = %v", got)
	}
	if got := Strings("zzz", []string{"a"}); got != nil {
		t.Fatalf("Rank(no match) = %v, want nil", got)
	}
}

func TestLeadPenaltyIsCapped(t *testing.T) {
	near, _ := Score("x", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaax")
	far, _ := Score("x", "aaaaaaaaaaax")
	if near != far || near != matchBase-maxLeadPenalty {
		t.Fatalf("lead penalty should cap at %d: %d vs %d", maxLeadPenalty, near, far)
	}
}

func TestBoundaryRunes(t *testing.T) {
	for _, sep := range []string{"-", "_", ":", "/", ".", " "} {
		on, _ := Score("b", "aaaa"+sep+"b")
		off, _ := Score("b", "aaaaxb")
		if on <= off {
			t.Errorf("boundary %q should score above a mid-word match: %d vs %d", sep, on, off)
		}
	}
}
