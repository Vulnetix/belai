package kanban

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormClauses(t *testing.T) {
	got, err := NormClauses([]string{"  export   to CSV ", "\x1b[1mkeep\x1b[0m the old format", "handle empty input"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "C1" || got[2].ID != "C3" || got[0].Text != "export to CSV" || got[1].Text != "keep the old format" {
		t.Fatalf("clauses: %+v", got)
	}
	for name, in := range map[string][]string{
		"none":     nil,
		"empty":    {"a", "  "},
		"repeat":   {"Same", " same "},
		"too many": make([]string, MaxClauses+1),
	} {
		if name == "too many" {
			for i := range in {
				in[i] = strings.Repeat("c", i+1)
			}
		}
		if _, err := NormClauses(in); !errors.Is(err, ErrClause) {
			t.Errorf("%s: want ErrClause, got %v", name, err)
		}
	}
	long, _ := NormClauses([]string{strings.Repeat("word ", 100)})
	if n := len([]rune(long[0].Text)); n > MaxClauseRunes {
		t.Fatalf("a clause has %d runes, cap %d", n, MaxClauseRunes)
	}
	exact := make([]string, MaxClauses)
	for i := range exact {
		exact[i] = strings.Repeat("c", i+1)
	}
	if got, err := NormClauses(exact); err != nil || got[MaxClauses-1].ID != "C12" {
		t.Fatalf("exactly %d clauses is fine: %v", MaxClauses, err)
	}
}

func TestNormCovers(t *testing.T) {
	cl := []Clause{{ID: "C1"}, {ID: "C2"}}
	got, err := NormCovers([]string{" c2 ", "C1", "C2"}, cl)
	if err != nil || len(got) != 2 || got[0] != "C2" || got[1] != "C1" {
		t.Fatalf("ids are normalised and not repeated: %v %v", got, err)
	}
	for _, bad := range []string{"C3", "C0", "x", "C01", "../C1", ""} {
		if _, err := NormCovers([]string{bad}, cl); !errors.Is(err, ErrClause) {
			t.Errorf("%q must be refused: %v", bad, err)
		}
	}
	if got, err := NormCovers(nil, cl); err != nil || len(got) != 0 {
		t.Fatalf("no covers is not an error here: %v %v", got, err)
	}
}

func TestCoverageGaps(t *testing.T) {
	parent := Item{ID: "p", Clauses: []Clause{{ID: "C1"}, {ID: "C2"}, {ID: "C3"}}}
	kids := []Item{
		{Parent: "p", Covers: []string{"C1"}},
		{Parent: "p", Covers: []string{"C3"}, Deleted: true},
		{Parent: "other", Covers: []string{"C2"}},
	}
	got := parent.CoverageGaps(kids)
	if len(got) != 2 || got[0] != "C2" || got[1] != "C3" {
		t.Fatalf("a deleted child and another card's child cover nothing: %v", got)
	}
	if g := (Item{}).CoverageGaps(kids); len(g) != 0 {
		t.Fatalf("a card with no clauses has no gaps: %v", g)
	}
}

func claimed(t *testing.T, s *Store, in ItemInput) Item {
	t.Helper()
	it, _, err := s.Add(in, prov)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimID(it.ID, ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestSetClausesNeedsTheClaim(t *testing.T) {
	s := testStore(t)
	it, _, _ := s.Add(ItemInput{Title: "request"}, prov)
	if _, err := s.SetClauses(it.ID, "w1", []string{"a"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("an unclaimed card refuses clauses: %v", err)
	}
	if _, err := s.ClaimID(it.ID, ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetClauses(it.ID, "w2", []string{"a"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("another worker cannot: %v", err)
	}
	got, err := s.SetClauses(it.ID, "w1", []string{"first", "second"})
	if err != nil || len(got.Clauses) != 2 {
		t.Fatalf("%v %v", got.Clauses, err)
	}
	got, _ = s.SetClauses(it.ID, "w1", []string{"only"})
	if len(got.Clauses) != 1 || got.Clauses[0].Text != "only" {
		t.Fatalf("recording again replaces: %v", got.Clauses)
	}
	if _, err := s.SetClauses(it.ID, "w1", nil); !errors.Is(err, ErrClause) {
		t.Fatalf("an empty list is refused: %v", err)
	}
}

func TestAddChecksCoversAgainstTheParentsClauses(t *testing.T) {
	s := testStore(t)
	parent := claimed(t, s, ItemInput{Title: "request"})
	if _, _, err := s.Add(ItemInput{Title: "child", Parent: parent.ID, Covers: []string{"C1"}}, prov); !errors.Is(err, ErrClause) {
		t.Fatalf("a parent with no clauses is covered by nothing: %v", err)
	}
	if _, err := s.SetClauses(parent.ID, "w1", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	child, _, err := s.Add(ItemInput{Title: "child", Parent: parent.ID, Covers: []string{"c2"}}, prov)
	if err != nil || len(child.Covers) != 1 || child.Covers[0] != "C2" {
		t.Fatalf("covers are checked and normalised: %v %v", child.Covers, err)
	}
	if _, _, err := s.Add(ItemInput{Title: "bad child", Parent: parent.ID, Covers: []string{"C9"}}, prov); !errors.Is(err, ErrClause) {
		t.Fatalf("an unknown clause is refused: %v", err)
	}
	kids, err := s.Children(parent.ID)
	if err != nil || len(kids) != 1 || kids[0].ID != child.ID {
		t.Fatalf("children: %v %v", kids, err)
	}
	got, _ := s.Get(parent.ID)
	if gaps := got.CoverageGaps(kids); len(gaps) != 1 || gaps[0] != "C1" {
		t.Fatalf("gaps %v", gaps)
	}
	if _, err := s.Children("K-nothing"); err == nil {
		t.Fatal("an unknown card has no children to list")
	}
}

func TestPulledCopyNeverCarriesOrReplacesClauses(t *testing.T) {
	local := Item{Clauses: []Clause{{ID: "C1", Text: "a"}}, Covers: []string{"C1"}}
	r := Item{Clauses: []Clause{{ID: "C1", Text: "changed"}, {ID: "C2"}}}
	keepAgent(&r, local)
	if len(r.Clauses) != 1 || r.Clauses[0].Text != "a" || len(r.Covers) != 1 {
		t.Fatalf("local clauses stand: %+v %+v", r.Clauses, r.Covers)
	}
	c := cloneItem(local)
	c.Clauses[0].Text = "x"
	c.Covers[0] = "C9"
	if local.Clauses[0].Text != "a" || local.Covers[0] != "C1" {
		t.Fatal("cloneItem shares clause or cover slices")
	}
}
