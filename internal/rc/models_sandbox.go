//go:build belai_sandbox

package rc

import "strings"

// In the Pix Sandbox build the stock Workers AI provider looks configured (the
// sandbox places credential placeholders) but its models are refused by the
// gateway, so the advertised list leaves it out and offers "builtin" instead.
func init() {
	hideProvider = func(name string) bool {
		return strings.EqualFold(name, "cloudflare-workers-ai")
	}
}
