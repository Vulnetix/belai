package kanban

import (
	"math/rand"
	"testing"
)

// hasStageRank reports whether l takes part in the staged lifecycle. The lists
// work happens in (InProgress) and waits in (Blocked) deliberately have no
// stage (StageRank returns the sentinel -1), so MovesBack never treats them as
// a step forward or back.
func hasStageRank(l List) bool { return StageRank(l) >= 0 }

// ranked lists every list that has a stage rank, in canonical lifecycle order:
// Backlog, Review, Done.
var ranked = func() []List {
	var out []List
	for _, l := range Lists {
		if hasStageRank(l) {
			out = append(out, l)
		}
	}
	return out
}()

// StageRank is a total order over the lists it ranks: distinct lists have
// distinct ranks, transitivity holds, and the sentinel -1 is shared by exactly
// the lists that are not stages.
func TestStageRankIsATotalOrder(t *testing.T) {
	for i, a := range Lists {
		for j, b := range Lists {
			if i == j {
				continue
			}
			if StageRank(a) == StageRank(b) && !(StageRank(a) == -1 && !hasStageRank(a) && !hasStageRank(b)) {
				t.Fatalf("StageRank(%s) == StageRank(%s) == %d, but %s is not the shared no-stage sentinel", a, b, StageRank(a), b)
			}
		}
	}

	for _, a := range ranked {
		for _, b := range ranked {
			for _, c := range ranked {
				if StageRank(a) < StageRank(b) && StageRank(b) < StageRank(c) && !(StageRank(a) < StageRank(c)) {
					t.Fatalf("StageRank not transitive: %s < %s < %s, yet %d !< %d", a, b, c, StageRank(a), StageRank(c))
				}
			}
		}
	}
}

// MovesBack agrees with StageRank: it is true exactly when the target ranks
// before the source, and it never reports a move from a list to itself.
func TestMovesBackAgreesWithStageRank(t *testing.T) {
	for _, from := range Lists {
		for _, to := range Lists {
			want := hasStageRank(from) && hasStageRank(to) && StageRank(to) < StageRank(from)
			if got := MovesBack(from, to); got != want {
				t.Fatalf("MovesBack(%s, %s) = %v, want %v (ranks %d, %d)", from, to, got, want, StageRank(from), StageRank(to))
			}
			if from == to && MovesBack(from, to) {
				t.Fatalf("MovesBack(%s, %s) = true for an identical list", from, to)
			}
		}
	}
}

// TestResolveRefsAndDepsDoneNeverPanic is a fuzz-style loop in the spirit of
// FuzzDecode: random generated inputs may error, but never panic.
func TestResolveRefsAndDepsDoneNeverPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"", "k-1234", "abcd", "k-abcd", "backlog", "  ", "zzzz", "deadbeef"}
	randString := func() string { return words[rng.Intn(len(words))] }
	board := Board{Items: []Item{{ID: "abcd"}}}

	for i := 0; i < 10_000; i++ {
		var refs []string
		for n := rng.Intn(8); n > 0; n-- {
			refs = append(refs, randString())
		}
		_, _ = resolveRefs(&board, refs)

		it := Item{DependsOn: refs}
		_ = depsDone(&board, it)
	}
}
