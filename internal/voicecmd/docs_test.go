package voicecmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// rowFor returns the Spoken keywords table cell that lists the phrases for a
// keyword, found by one phrase it must contain.
func keywordRows(t *testing.T) []string {
	t.Helper()
	doc := docparity.Read(t, "docs/voice.md")
	section := regexp.MustCompile(`(?s)## Spoken keywords(.*?)## Spoken instructions`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the Spoken keywords section moved")
	}
	var rows []string
	for _, line := range strings.Split(section[1], "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| Say") && !strings.HasPrefix(line, "| ---") {
			rows = append(rows, strings.SplitN(strings.TrimPrefix(line, "| "), " | ", 2)[0])
		}
	}
	return rows
}

// TestVoicePageListsEveryKeywordPhrase keeps the first column of the Spoken
// keywords table equal to the vocabulary the matcher accepts: a phrase the code
// treats as a keyword must be on the page, in the row of its keyword, because
// two of them allow once and for ever.
func TestVoicePageListsEveryKeywordPhrase(t *testing.T) {
	rows := keywordRows(t)
	byKeyword := map[Keyword][]string{}
	for phrase, k := range keywords {
		byKeyword[k] = append(byKeyword[k], phrase)
	}
	for k, phrases := range byKeyword {
		for _, p := range phrases {
			found := false
			for _, row := range rows {
				for _, cell := range strings.Split(row, ", ") {
					if cell == p {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("the keyword table does not list %q (%s)", p, k)
			}
		}
	}
	// Every phrase on the page is one the matcher accepts.
	for _, row := range rows {
		if strings.HasPrefix(row, "option 1") {
			continue
		}
		for _, cell := range strings.Split(row, ", ") {
			if _, ok := keywords[cell]; !ok {
				t.Errorf("the page lists %q, which is not a keyword", cell)
			}
		}
	}
}

// TestVoicePageStatesTheOptionVocabulary pins the option words and homophones.
func TestVoicePageStatesTheOptionVocabulary(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/voice.md")), " ")
	for _, want := range []string{
		"can be option, number, choice, select, choose or pick",
		"homophones won, to, too and for count as 1, 2, 2 and 4",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not say %q", want)
		}
	}
	for _, c := range []struct {
		say string
		n   int
	}{{"option 1", 1}, {"number 2", 2}, {"choice three", 3}, {"select four", 4}, {"choose won", 1}, {"pick to", 2}, {"pick too", 2}, {"option for", 4}, {"3", 3}} {
		if k, n, ok := MatchKeyword(c.say); !ok || k != Option || n != c.n {
			t.Errorf("%q = %v %d %v, want option %d", c.say, k, n, ok, c.n)
		}
	}
	if MaxOption != 4 {
		t.Errorf("MaxOption = %d, the page says option 4", MaxOption)
	}
	if _, _, ok := MatchKeyword("option 5"); ok {
		t.Error("option 5 matched, the page stops at 4")
	}
}

// TestVoicePageStatesTheWakeWord pins the opening words and the name rules.
func TestVoicePageStatesTheWakeWord(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/voice.md")), " ")
	for _, want := range []string{
		`"hay", "hi", "a", "okay" or "ok"`,
		`one letter off "belay" or "belai"`,
		`"They said hey belay" and "hey there belay" do not match`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not say %q", want)
		}
	}
	for _, say := range []string{"hey belay fix it", "hay belay", "hi belai", "a belay", "okay belay", "ok belay", "hey belays", "hey bellay", "hey belly", "hey, Belay, fix the failing test"} {
		if _, ok := MatchWake(say); !ok {
			t.Errorf("%q did not match the wake word the page describes", say)
		}
	}
	for _, say := range []string{"They said hey belay", "hey there belay", "hey", "belay", "hello belay", "hey beta", "hey belayer ok"} {
		if _, ok := MatchWake(say); ok {
			t.Errorf("%q matched, but the page says it does not", say)
		}
	}
	if rest, _ := MatchWake("Hey, Belay, fix the failing test"); rest != "fix the failing test" {
		t.Errorf("the phrase left %q behind, the page says only the instruction", rest)
	}
}
