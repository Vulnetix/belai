//go:build belai_sandbox

package models

import "strings"

// In the Pix Sandbox build, Pix Smart accepts image input and Pix Fast does
// not. The ids are the labels the "builtin" provider advertises.
func init() {
	extraVision = func(providerName, id string) bool {
		return strings.EqualFold(providerName, "builtin") && id == "pix-smart"
	}
}
