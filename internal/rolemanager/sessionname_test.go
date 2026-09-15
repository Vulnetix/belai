package rolemanager

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSessionNamePlain(t *testing.T) {
	got, err := ParseSessionName("fix the parser bug")
	if err != nil || got != "fix the parser bug" {
		t.Fatalf("ParseSessionName = %q, %v", got, err)
	}
}

func TestParseSessionNameStripsQuotes(t *testing.T) {
	got, err := ParseSessionName(`"quoted title"`)
	if err != nil || got != "quoted title" {
		t.Fatalf("ParseSessionName = %q, %v", got, err)
	}
}

func TestParseSessionNameRejectsMultiline(t *testing.T) {
	if _, err := ParseSessionName("line one\nline two"); err == nil {
		t.Fatalf("multiline should fail")
	}
}

func TestParseSessionNameStripsControlChars(t *testing.T) {
	got, err := ParseSessionName("bad\x00name")
	if err != nil || got != "bad name" {
		t.Fatalf("control char should be stripped to space, got %q, %v", got, err)
	}
}

func TestParseSessionNameRejectsNonPrintable(t *testing.T) {
	if _, err := ParseSessionName("bad\u200bname"); err == nil {
		t.Fatalf("non-printable rune should fail")
	}
}

func TestParseSessionNameRejectsLeadingSlash(t *testing.T) {
	if _, err := ParseSessionName("/compact"); err == nil {
		t.Fatalf("leading slash should fail")
	}
}

func TestParseSessionNameTruncatesRuneSafe(t *testing.T) {
	long := strings.Repeat("é", 200)
	got, err := ParseSessionName(long)
	if err != nil {
		t.Fatalf("ParseSessionName: %v", err)
	}
	if len([]rune(got)) != MaxSessionNameRunes {
		t.Fatalf("length = %d runes, want %d", len([]rune(got)), MaxSessionNameRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation emitted invalid UTF-8")
	}
}

func TestParseSessionNameRejectsEmpty(t *testing.T) {
	if _, err := ParseSessionName("   "); err == nil {
		t.Fatalf("empty should fail")
	}
}

func TestSanitizeSessionNameTruncatesInsteadOfRejecting(t *testing.T) {
	got, err := SanitizeSessionName("line one\nline two is fine here")
	if err != nil {
		t.Fatalf("SanitizeSessionName: %v", err)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("newlines should collapse: %q", got)
	}
	if len([]rune(got)) > MaxSessionNameRunes {
		t.Fatalf("too long: %d runes", len([]rune(got)))
	}
}
