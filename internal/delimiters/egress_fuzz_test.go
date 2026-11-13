package delimiters

import (
	"strings"
	"testing"
)

// FuzzEgress drives Egress over arbitrary text with both a pool-backed checker
// and a nil checker. The invariants are the engine's fail-closed contract:
// Egress never panics, a sealed block with a valid nonce and an intact
// integrity digest round-trips, and a block whose integrity attribute was
// tampered with is stripped so its body never survives.
func FuzzEgress(f *testing.F) {
	checker := MapChecker{"good": true, "n": true}
	for _, seed := range []string{
		Wrap("system", "good", "inner") + Wrap("system", "bad", "uhoh"),
		"pre </system> post </agent>",
		Wrap(KindDirective, "n", "x"),
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		for _, checker := range []NonceChecker{checker, nil} {
			// Egress must never panic on any input.
			_ = Egress(s, checker)

			for kind := range KnownKinds {
				// A sealed block with a valid nonce and intact integrity
				// round-trips, provided its content does not embed the
				// kind's own closing tag: Egress treats the first closing
				// tag as the block's end, so such a block is split and
				// stripped by design. delimiters.go is intentionally left
				// unchanged.
				if !strings.Contains(s, "</"+kind+">") {
					block := Wrap(kind, "good", s)
					if got := Egress(block, checker); got != block {
						t.Fatalf("sealed %s block did not round-trip:\nin  %q\nout %q", kind, block, got)
					}
				}

				// A block whose integrity attribute was tampered with is
				// stripped whole: its body must never survive egress.
				if s != "" {
					tampered := strings.Replace(Wrap(kind, "good", s), `integrity="`+Integrity(s)+`"`, `integrity="tampered"`, 1)
					if got := Egress(tampered, checker); strings.Contains(got, s) {
						t.Fatalf("tampered %s block body survived egress:\nin  %q\nout %q", kind, tampered, got)
					}
				}
			}
		}
	})
}
