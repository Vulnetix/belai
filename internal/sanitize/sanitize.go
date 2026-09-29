// Package sanitize is the harness's deterministic input cleaning, one
// constructor per sink. Text that goes to a model, a terminal line, a decision
// backend, a file path, a header or a URL is not the same problem, so each has
// its own function with its own rules; none of them asks a model anything.
//
//   - [Sanitize]      delimiter markup only (the base of the others).
//   - [Text]          free text bound for a model or the transcript.
//   - [Line]          one line of UI, log or notification text.
//   - [Ident]         an identifier or label.
//   - [ForDecision]   state for a decision backend (Jev), as a [DecisionText].
//   - [PathText]      the lexical checks before a path is resolved.
//   - [Header]        an HTTP header value.
//
// Shell commands live in internal/shellsafe and URLs in internal/netguard; the
// tools package picks the right one per argument from its declared Format.
//
// The delimiter scrub mirrors the delimiter engine (internal/delimiters) and
// shares its KnownKinds set; the two must stay in sync.
package sanitize

// Sanitize removes every harness delimiter tag (opening and closing) and
// strips nonce/integrity attributes from any remaining tag. Normal text and
// non-harness tags pass through unchanged except for those two attributes.
//
// Tags are matched on a folded form of the text: case, fullwidth and small
// form angle brackets, HTML character references and invisible runes inside a
// tag do not hide it, and the scan repeats until nothing changes so removing
// one tag cannot assemble another. The function is idempotent.
func Sanitize(s string) string {
	if !scanMarkup(s) {
		return s
	}
	return stripDelimiters(s)
}
