package rolemanager

import (
	"errors"
	"strings"
	"unicode"
)

// sessionNameSystemPrompt instructs the classifier to produce a short title.
const sessionNameSystemPrompt = `You name coding sessions. You will be shown the first message a user sent to a coding assistant. Reply with a short title for the session — three to six words, no quotes, no punctuation at the end, no explanation. Reply with the title on a single line and nothing else.`

// MaxSessionNameRunes bounds a session name for the status bar.
const MaxSessionNameRunes = 48

// BuildSessionNamePayload constructs the naming request. Tools, Skills, and
// Agent are always empty; the model sees only the user's first message.
// caveman voices the title; ParseSessionName's rules apply either way.
func BuildSessionNamePayload(firstUserMessage string, caveman bool) ClassifierPayload {
	return ClassifierPayload{
		System:  withCavemanVoice(sessionNameSystemPrompt, caveman),
		User:    firstUserMessage,
		UseCase: UseCaseSessionName,
	}
}

// ParseSessionName validates a model-produced name and fails closed: a
// malformed reply leaves the session unnamed rather than taking a mangled or
// attacker-chosen title.
func ParseSessionName(raw string) (string, error) {
	s, err := sanitizeName(raw, true)
	if err != nil {
		record(EventSessionName, "invalid", "", "", 0)
		return "", err
	}
	record(EventSessionName, "valid", "", "", 0)
	return s, nil
}

// SanitizeSessionName normalises a user-supplied name from /rename using the
// same rules, minus the single-line rejection (it truncates instead).
func SanitizeSessionName(raw string) (string, error) {
	return sanitizeName(raw, false)
}

func sanitizeName(raw string, rejectMultiline bool) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("session name is empty")
	}
	if rejectMultiline {
		nonEmpty := 0
		for _, line := range strings.Split(s, "\n") {
			if strings.TrimSpace(line) != "" {
				nonEmpty++
			}
		}
		if nonEmpty > 1 {
			return "", errors.New("session name must be a single line")
		}
	}

	// Strip control characters (including newlines/tabs) to spaces.
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	// Collapse whitespace.
	s = strings.Join(strings.Fields(s), " ")
	// Strip matched wrapping quotes/backticks.
	s = stripWrappingQuotes(s)

	if strings.HasPrefix(s, "/") {
		return "", errors.New("session name must not start with /")
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", errors.New("session name contains non-printable characters")
		}
	}

	runes := []rune(s)
	if len(runes) > MaxSessionNameRunes {
		s = string(runes[:MaxSessionNameRunes])
	}
	if strings.TrimSpace(s) == "" {
		return "", errors.New("session name is empty")
	}
	return s, nil
}

func stripWrappingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
		return s[1 : len(s)-1]
	}
	return s
}
