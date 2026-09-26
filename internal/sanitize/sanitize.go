// Package sanitize neutralises harness delimiter markup in untrusted content.
// Read/WebSearch/WebFetch outputs are sanitised *before* they are ever wrapped
// in harness delimiters, so adversarial text cannot forge a system/agent/plan/
// goal block and get promoted into trusted context.
//
// The tag patterns below mirror the delimiter engine's patterns
// (internal/delimiters) and share its KnownKinds set; they must stay in sync.
package sanitize

import (
	"regexp"

	"github.com/vulnetix/belai/internal/delimiters"
)

var (
	openTagRe   = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)([^>]*)>`)
	closeTagRe  = regexp.MustCompile(`</([a-zA-Z][a-zA-Z0-9_-]*)>`)
	attrStripRe = regexp.MustCompile(`\s+(?:nonce|integrity)="[^"]*"`)
)

// Sanitize removes every harness delimiter tag (opening and closing) and
// strips nonce/integrity attributes from any remaining tag. Normal text and
// non-harness tags pass through unchanged except for those two attributes.
func Sanitize(s string) string {
	s = closeTagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := closeTagRe.FindStringSubmatch(m)
		if len(sub) == 2 && delimiters.KnownKinds[sub[1]] {
			return ""
		}
		return m
	})
	s = openTagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := openTagRe.FindStringSubmatch(m)
		if len(sub) == 3 && delimiters.KnownKinds[sub[1]] {
			return ""
		}
		return attrStripRe.ReplaceAllString(m, "")
	})
	return s
}
