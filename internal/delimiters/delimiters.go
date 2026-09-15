// Package delimiters implements the harness's integrity-checked delimiter
// engine. Every harness-generated block carries a random nonce plus a SHA-256
// integrity hash of its enclosed content. On egress (before any payload leaves
// for a model provider) the engine verifies every block and strips any block
// that is structurally invalid, carries an unknown nonce, or fails its
// integrity hash — so unverified text can never be promoted into
// system/agent/plan/goal blocks.
package delimiters

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// KindAttachment is delimited user-supplied content referenced by @file or
// !shell and sealed at egress with a nonce from the active pool.
const KindAttachment = "attachment"

// KindExploration is the delimiter kind for explore-subagent findings. Like
// attachments, a sealed exploration block survives egress while an unsealed or
// forged one is stripped whole.
const KindExploration = "exploration"

// KnownKinds is the set of harness block kinds the engine manages. Tags with
// any other kind (e.g. arbitrary HTML in user content) are left untouched.
var KnownKinds = map[string]bool{
	"system":        true,
	"agent":         true,
	"plan":          true,
	"goal":          true,
	"tools":         true,
	"skills":        true,
	"hooks":         true,
	KindAttachment:  true,
	KindExploration: true,
}

// NonceChecker reports whether a nonce is currently valid (present in the
// active pool).
type NonceChecker interface {
	Valid(nonce string) bool
}

// CheckerFunc adapts a func to NonceChecker.
type CheckerFunc func(nonce string) bool

// Valid implements NonceChecker.
func (f CheckerFunc) Valid(nonce string) bool { return f(nonce) }

// MapChecker validates nonces against a static set (used in tests).
type MapChecker map[string]bool

// Valid implements NonceChecker.
func (m MapChecker) Valid(nonce string) bool { return m[nonce] }

// Open returns the opening tag for a block, carrying its nonce and the
// SHA-256 integrity hash of its content.
func Open(kind, nonce, content string) string {
	return fmt.Sprintf(`<%s nonce="%s" integrity="%s">`, kind, nonce, Integrity(content))
}

// Close returns the closing tag for a block kind.
func Close(kind string) string {
	return fmt.Sprintf("</%s>", kind)
}

// Wrap returns a fully delimited block: opening tag, content, closing tag.
func Wrap(kind, nonce, content string) string {
	return Open(kind, nonce, content) + content + Close(kind)
}

// Integrity returns the lowercase hex SHA-256 digest of content.
func Integrity(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

var (
	openTagRe  = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)([^>]*)>`)
	closeTagRe = regexp.MustCompile(`</([a-zA-Z][a-zA-Z0-9_-]*)>`)
	attrRe     = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9_-]*)="([^"]*)"`)
)

// Egress validates every harness delimiter block in text and strips invalid
// ones. A block is valid iff it has a nonce, the nonce is accepted by the
// checker (when non-nil), and — when an integrity attribute is present — the
// integrity digest matches its enclosed content. Invalid blocks are removed
// in their entirety (opening tag, content, and closing tag). Unmatched
// closing tags of known kinds are removed too; unknown-kind tags are left
// untouched.
//
// <attachment> blocks are handled like other harness kinds: a sealed
// attachment survives, while an unsealed or forged one is stripped whole.
func Egress(text string, checker NonceChecker) string {
	var b strings.Builder
	pos := 0
	for pos < len(text) {
		loc := openTagRe.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			b.WriteString(stripStrayCloses(text[pos:]))
			break
		}
		openStart := pos + loc[0]
		openEnd := pos + loc[1]
		kind := text[pos+loc[2] : pos+loc[3]]
		attrs := text[pos+loc[4] : pos+loc[5]]

		b.WriteString(stripStrayCloses(text[pos:openStart]))

		if !KnownKinds[kind] {
			// Not a harness delimiter; keep the tag verbatim.
			b.WriteString(text[openStart:openEnd])
			pos = openEnd
			continue
		}

		closeRe := regexp.MustCompile(`</` + regexp.QuoteMeta(kind) + `>`)
		closeLoc := closeRe.FindStringIndex(text[openEnd:])
		if closeLoc == nil {
			// Opening tag with no matching close: drop the opening tag.
			pos = openEnd
			continue
		}
		closeStart := openEnd + closeLoc[0]
		closeEnd := openEnd + closeLoc[1]
		content := text[openEnd:closeStart]

		if valid(attrs, content, checker, kind) {
			b.WriteString(text[openStart:closeEnd])
		}
		pos = closeEnd
	}
	return b.String()
}

// stripStrayCloses removes unmatched closing tags of known kinds only.
func stripStrayCloses(s string) string {
	return closeTagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := closeTagRe.FindStringSubmatch(m)
		if len(sub) == 2 && KnownKinds[sub[1]] {
			return ""
		}
		return m
	})
}

// valid applies the three fail-closed checks to one block. Attachment
// blocks must carry an integrity attribute because they wrap untrusted
// content; other kinds retain their historical "nonce only is enough"
// behaviour for backward compatibility.
func valid(attrs, content string, checker NonceChecker, kind string) bool {
	parsed := parseAttrs(attrs)
	nonce, ok := parsed["nonce"]
	if !ok || nonce == "" {
		return false
	}
	if checker != nil && !checker.Valid(nonce) {
		return false
	}
	integ, present := parsed["integrity"]
	if kind == KindAttachment {
		if !present || integ == "" {
			return false
		}
		return integ == Integrity(content)
	}
	if present && integ != "" {
		return integ == Integrity(content)
	}
	return true
}

// parseAttrs parses name="value" attributes.
func parseAttrs(s string) map[string]string {
	m := map[string]string{}
	for _, match := range attrRe.FindAllStringSubmatch(s, -1) {
		m[match[1]] = match[2]
	}
	return m
}
