package agentfiles

import "github.com/vulnetix/belai/internal/secretshape"

// HoldsSecret reports whether b holds a private key block or a known token.
// The shapes live in secretshape so the knowledge store can share them.
func HoldsSecret(b []byte) bool { return secretshape.Holds(b) }
