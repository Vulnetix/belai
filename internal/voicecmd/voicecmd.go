// Package voicecmd recognises spoken control phrases in a transcript: the
// "Hey, Belay" wake word and a closed vocabulary of isolated keywords (stop,
// option 2, submit, skip, approve, approve always, deny).
//
// Matching is deterministic string work on the recognised text. No model is
// asked, and a phrase only matches as the whole utterance (keywords) or at its
// very start (wake word), so ordinary speech that merely contains a keyword
// never matches.
package voicecmd

import (
	"strconv"
	"strings"
	"unicode"
)

// Keyword is a spoken control word.
type Keyword string

// The closed keyword vocabulary.
const (
	Stop          Keyword = "stop"
	Submit        Keyword = "submit"
	Skip          Keyword = "skip"
	Approve       Keyword = "approve"
	ApproveAlways Keyword = "approve_always"
	Deny          Keyword = "deny"
	Option        Keyword = "option"
)

// MaxOption is the highest option number a phrase may name.
const MaxOption = 4

var keywords = map[string]Keyword{
	"stop": Stop, "cancel": Stop, "interrupt": Stop,
	"submit": Submit, "send": Submit, "done": Submit,
	"skip":    Skip,
	"approve": Approve, "approved": Approve, "allow": Approve, "yes": Approve,
	"approve always": ApproveAlways, "always approve": ApproveAlways,
	"always allow": ApproveAlways, "allow always": ApproveAlways,
	"approved always": ApproveAlways,
	"deny":            Deny, "denied": Deny, "reject": Deny, "no": Deny,
}

var numbers = map[string]int{
	"1": 1, "one": 1, "won": 1,
	"2": 2, "two": 2, "to": 2, "too": 2,
	"3": 3, "three": 3,
	"4": 4, "four": 4, "for": 4,
}

// Normalize lowercases text, turns punctuation into spaces and collapses
// runs of whitespace.
func Normalize(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// "belay's" stays one word
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// MatchKeyword reports the keyword an utterance consists of entirely. For
// Option the number (1 to MaxOption) is returned as n. A leading "please" and
// a trailing "please" are ignored.
func MatchKeyword(text string) (k Keyword, n int, ok bool) {
	s := Normalize(text)
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "please "), " please"))
	if s == "" {
		return "", 0, false
	}
	if k, ok := keywords[s]; ok {
		return k, 0, true
	}
	for _, p := range []string{"option ", "number ", "choice ", "select ", "choose ", "pick "} {
		if rest, found := strings.CutPrefix(s, p); found {
			rest = strings.TrimPrefix(rest, "number ")
			rest = strings.TrimPrefix(rest, "option ")
			if v, found := numbers[rest]; found {
				return Option, v, true
			}
			return "", 0, false
		}
	}
	if v, err := strconv.Atoi(s); err == nil && v >= 1 && v <= MaxOption {
		return Option, v, true
	}
	return "", 0, false
}

var wakeHeads = map[string]bool{"hey": true, "hay": true, "hi": true, "a": true, "hey,": true, "okay": true, "ok": true}

// MatchWake reports whether text starts with the wake word "Hey, Belay" and
// returns what follows it. The name tolerates the recogniser's usual
// misspellings (one edit from "belay" or "belai"). The phrase is stripped so
// nothing after this point sees it.
func MatchWake(text string) (rest string, ok bool) {
	words := strings.Fields(Normalize(text))
	if len(words) < 2 || !wakeHeads[words[0]] {
		return "", false
	}
	name := strings.TrimSuffix(words[1], "s")
	if !nearName(name) {
		return "", false
	}
	return strings.Join(words[2:], " "), true
}

func nearName(w string) bool {
	if len(w) < 5 || len(w) > 8 {
		return false
	}
	return edits(w, "belay") <= 1 || edits(w, "belai") <= 1 || w == "bellay" || w == "belly"
}

// edits is the Levenshtein distance between two short ASCII words.
func edits(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}
