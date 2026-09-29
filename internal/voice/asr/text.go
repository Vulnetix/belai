package asr

import (
	"strings"
	"unicode"
)

// tidy trims a decoded transcript and returns "" when it is only a sound
// annotation. Whisper writes "(wind blowing)", "[BLANK_AUDIO]" or a lone
// "you" for silence and noise; none of that is something the user said.
func tidy(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if annotationOnly(s) {
		return ""
	}
	if stockSilencePhrase[strings.ToLower(strings.Trim(s, " .!?,"))] {
		return ""
	}
	return s
}

// stockSilencePhrase holds what the model writes for near-silence. A person
// may say these too, but as the whole of an utterance they are far more often
// a hallucination than dictation.
var stockSilencePhrase = map[string]bool{
	"you": true, "thank you": true, "thanks for watching": true, "bye": true,
}

// annotationOnly reports whether every word of s sits inside brackets,
// parentheses, asterisks or music notes.
func annotationOnly(s string) bool {
	depth := 0
	starred := false
	sawLetterOutside := false
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			continue
		case '*':
			starred = !starred
			continue
		case '♪':
			continue
		}
		if depth == 0 && !starred && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			sawLetterOutside = true
		}
	}
	return !sawLetterOutside
}
