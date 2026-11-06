// Package secretshape recognises the shapes of a credential that has no
// business in a document an agent searches, a library copy or a source index:
// a private key block and the well-known token formats. It is a floor under
// the host's classifier, not a scanner, and it lives in a leaf package so the
// knowledge store and the library file reader share one list.
package secretshape

import "regexp"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
}

// Holds reports whether b holds a private key block or a known token.
func Holds(b []byte) bool {
	for _, re := range patterns {
		if re.Match(b) {
			return true
		}
	}
	return false
}
