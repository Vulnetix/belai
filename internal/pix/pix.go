// Package pix holds the Pix mascot drawing that a generated agent avatar is a
// variant of. The drawing is the website's Pix without its animation, so every
// element in it is one the SVG guard (internal/svgguard) admits, and a model is
// shown exactly the picture it may change.
package pix

import _ "embed"

// SVG is the static Pix drawing, 320 by 320 units in the viewBox 96 26 320 320.
//
//go:embed pix.svg
var SVG []byte

// TemplateVersion names this drawing. It is sent with an avatar request so the
// website can tell a variant of an older Pix from one of the current drawing.
const TemplateVersion = "1"
