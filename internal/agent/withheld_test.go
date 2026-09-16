package agent

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// A provider rejection body is kilobytes of JSON and a server-side stack
// trace. Promoting it verbatim floods both the model's context and the
// transcript with text neither can act on, so the placeholder clips it.
func TestClassifierWithheldClipsLongProviderBody(t *testing.T) {
	body := `provider returned 400: {"errors":[{"message":"AiError: AiError: {\"object\":\"error\",` +
		"\n" + strings.Repeat("validation error detail ", 100) + "\n}]}"
	got := classifierWithheld("Bash", errors.New(body))

	if !strings.HasPrefix(got, `tool result withheld: classifier error for "Bash": `) {
		t.Fatalf("missing prefix: %q", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("placeholder spans multiple lines: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > 256 {
		t.Fatalf("placeholder is %d runes, want <= 256: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped placeholder should end in an ellipsis: %q", got)
	}
	if !strings.Contains(got, "provider returned 400") {
		t.Fatalf("placeholder dropped the actionable head of the error: %q", got)
	}
}

// A short error is already actionable and survives intact.
func TestClassifierWithheldKeepsShortError(t *testing.T) {
	got := classifierWithheld("Read", errors.New("request: connection refused"))
	want := `tool result withheld: classifier error for "Read": request: connection refused`
	if got != want {
		t.Fatalf("classifierWithheld = %q, want %q", got, want)
	}
}
