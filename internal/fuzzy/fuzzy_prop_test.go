package fuzzy

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"unicode"
)

// propAlphabet keeps generated inputs small enough that both branches of
// every property are hit often: 'a', 'b', 'c' build plain runs, 'A' checks
// case-insensitivity against the reference helper, and ' ', '\t' are boundary
// runes the scorer rewards and penalises.
var propAlphabet = []rune{'a', 'b', 'c', ' ', '\t', 'A'}

// propSeed pins each round to its own seeded generator so failures reproduce
// across runs, CI jobs and -count reruns.
func propSeed(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func propRunes(rng *rand.Rand, maxLen int) []rune {
	out := make([]rune, rng.Intn(maxLen+1))
	for i := range out {
		out[i] = propAlphabet[rng.Intn(len(propAlphabet))]
	}
	return out
}

// isSubsequence is the reference for Score's documented match contract: the
// query matches when every query rune appears in the target, in order,
// case-insensitively. It shares no code with Score.
func isSubsequence(query, target []rune) bool {
	lq := make([]rune, len(query))
	for i, r := range query {
		lq[i] = unicode.ToLower(r)
	}
	lt := make([]rune, len(target))
	for i, r := range target {
		lt[i] = unicode.ToLower(r)
	}
	j := 0
	for _, r := range lt {
		if j < len(lq) && lq[j] == r {
			j++
		}
	}
	return j == len(lq)
}

// genFrom builds a query/target pair. Roughly a third of the pairs leave both
// keys absent so the no-match branch is exercised; otherwise the query starts
// as a shuffled half-copy of the target, padded from the alphabet, which
// makes matches common without making them certain.
func genFrom(rng *rand.Rand, alphabet []rune, maxLen int) (query, target []rune) {
	target = propRunes(rng, maxLen)
	if rng.Intn(3) == 0 {
		query = propRunes(rng, maxLen)
		if rng.Intn(2) == 0 {
			query = append(query, propRunes(rng, maxLen)...)
		}
		return query, target
	}
	query = make([]rune, 0, len(target))
	for _, r := range target {
		if rng.Intn(2) == 0 {
			query = append(query, r)
		}
	}
	for i := rng.Intn(maxLen + 1); i > 0; i-- {
		query = append(query, alphabet[rng.Intn(len(alphabet))])
	}
	rng.Shuffle(len(query), func(i, j int) { query[i], query[j] = query[j], query[i] })
	return query, target
}

func propCase(query, target []rune) string {
	return fmt.Sprintf("query %q target %q", string(query), string(target))
}

// TestScoreMatchesSubsequenceInvariant pins Score against the reference scan
// on generated pairs: ok exactly when the query is a case-insensitive rune
// subsequence of the target, empty queries always match with score 0, and
// found scores are non-negative. Score passes the gap penalty through
// unbounded (fuzzy.go scoreFrom), so the last property only holds on short
// targets; 16 runes stays inside the tens-of-runes candidate domain the
// package serves, where every found score is at least 8.
func TestScoreMatchesSubsequenceInvariant(t *testing.T) {
	const maxLen = 16
	for i := 0; i < 400; i++ {
		query, target := genFrom(propSeed(int64(1_000+i)), propAlphabet, maxLen)
		want := isSubsequence(query, target)
		got, ok := Score(string(query), string(target))
		if ok != want {
			t.Errorf("ok = %v, reference scan says %v for %s", ok, want, propCase(query, target))
			continue
		}
		if !ok {
			continue
		}
		if got < 0 {
			t.Errorf("found score %d is negative for %s", got, propCase(query, target))
		}
		if len(query) == 0 && got != 0 {
			t.Errorf("empty query scored %d, want 0, for %s", got, propCase(query, target))
		}
	}
}

// TestScoreNeverLowersWhenTargetGrows pins the ranking-side invariant that
// extending a target by one rune never scores a query lower than the same
// query on the shorter target: a longer target can lose the match entirely,
// but a match it keeps cannot get worse.
func TestScoreNeverLowersWhenTargetGrows(t *testing.T) {
	const maxLen = 16
	for i := 0; i < 200; i++ {
		query, target := genFrom(propSeed(int64(7_000+i)), propAlphabet, maxLen)
		for _, r := range propAlphabet {
			grown := append(append([]rune{}, target...), r)
			before, ok0 := Score(string(query), string(target))
			after, ok1 := Score(string(query), string(grown))
			if ok0 && ok1 && after < before {
				t.Errorf("appending %q lowered %q from %d to %d on %s",
					r, string(query), before, after, propCase(query, target))
			}
		}
	}
}

// TestRankTieStability extends TestRankKeepsInputOrderOnTies to generated
// inputs: equal scores keep input order, and a candidate whose keys do not
// match the query is never ranked alongside candidates whose keys do.
func TestRankTieStability(t *testing.T) {
	const maxLen = 12
	// Pure ties: identical items score identically, so a matching query must
	// return every item in input order and a non-matching one none at all.
	for i := 0; i < 100; i++ {
		rng := propSeed(int64(11_000 + i))
		word := string(propRunes(rng, maxLen))
		items := make([]string, 2+rng.Intn(6))
		for j := range items {
			items[j] = word
		}
		query := string(propRunes(rng, maxLen))
		got := Strings(query, items)
		if _, ok := Score(query, word); ok {
			if !reflect.DeepEqual(got, items) {
				t.Errorf("Rank(%q) reordered tied items: %v, want %v", query, got, items)
			}
		} else if got != nil {
			t.Errorf("Rank(%q) matched items whose key does not match: %v", query, got)
		}
	}
	// Mixed: distinct items where some match and some do not. Non-matching
	// items are dropped, and a matching item keeps its input order unless a
	// later item strictly outscores it.
	for i := 0; i < 200; i++ {
		rng := propSeed(int64(13_000 + i))
		n := 2 + rng.Intn(6)
		items := make([]string, n)
		for j := range items {
			// The zero-padded index keeps items distinct so positions in the
			// output identify inputs.
			items[j] = fmt.Sprintf("%02d%s", j, string(propRunes(rng, maxLen)))
		}
		query := string(propRunes(rng, maxLen))
		got := Rank(query, items, func(s string) []string { return []string{s} })

		scores := make([]int, n)
		matched := make([]bool, n)
		nMatch := 0
		for j, it := range items {
			scores[j], matched[j] = Score(query, it)
			if matched[j] {
				nMatch++
			}
		}
		if len(got) != nMatch {
			t.Fatalf("Rank(%q) returned %d items, %d keys match: %v", query, len(got), nMatch, got)
		}
		pos := make(map[string]int, n)
		for idx, it := range got {
			pos[it] = idx
		}
		for j, it := range items {
			p, in := pos[it]
			if in != matched[j] {
				t.Fatalf("Rank(%q) membership wrong for %q (index %d): in output %v, key matches %v", query, it, j, in, matched[j])
			}
			if !in {
				continue
			}
			for k := 0; k < j; k++ {
				if !matched[k] {
					continue
				}
				// The earlier item stays ahead unless this one outscores it.
				if pe := pos[items[k]]; pe > p && !(scores[j] > scores[k]) {
					t.Errorf("Rank(%q) put %q (score %d) above %q (score %d) without outscoring it",
						query, it, scores[j], items[k], scores[k])
				}
			}
		}
	}
}

// FuzzScore cross-checks Score against the reference scan on raw fuzzer
// strings: the match verdict must agree with a plain case-insensitive
// subsequence walk, whatever the bytes are, and neither side may panic.
func FuzzScore(f *testing.F) {
	for _, q := range []string{"", "prompts", "PMT", "ab c", "\tA", "bogus", "日本語", "\xff\xfe"} {
		for _, t := range []string{"", "prompts", "agent:belai:debug", "add-dir", "a\tb\tc", "  a  b "} {
			f.Add(q, t)
		}
	}
	f.Fuzz(func(t *testing.T, query, target string) {
		want := isSubsequence([]rune(query), []rune(target))
		if _, ok := Score(query, target); ok != want {
			t.Fatalf("Score(%q, %q) ok = %v, want %v", query, target, ok, want)
		}
	})
}
