package sanitize

import (
	"regexp"
	"strings"
)

var (
	inertMention = regexp.MustCompile(`@([A-Za-z0-9_])`)
	inertIssue   = regexp.MustCompile(`#([0-9])`)
	inertScheme  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,15}):(//)`)
	inertWWW     = regexp.MustCompile(`(?i)\bwww\.`)
)

// Inert cleans text that is about to be posted to a forge as a comment, where
// it is rendered as Markdown and read by other people: [Text] first, then the
// parts a forge would act on are defused. An @name no longer notifies anyone
// (the at sign becomes the fullwidth one), #123 no longer links an issue,
// scheme://host and www.host no longer become links, a Markdown link target
// loses its parenthesis, and angle brackets become entities so HTML and
// autolinks do not render. The text still reads the same to a person. The
// result is capped at max runes, or not capped when max is zero or less.
func Inert(s string, max int) string {
	s = Text(s)
	s = inertMention.ReplaceAllString(s, "＠$1")
	s = inertIssue.ReplaceAllString(s, "＃$1")
	s = inertScheme.ReplaceAllString(s, "$1[:]$2")
	s = inertWWW.ReplaceAllString(s, "www[.]")
	s = strings.ReplaceAll(s, "](", "] (")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return capRunes(s, max, "")
}
