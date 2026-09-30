package acp

import (
	"runtime"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/version"
)

// tagline is the brand line the TUI's compact header carries.
const tagline = "a safer LLM harness · vulnetix.com"

// headerText is the TUI's compact header as plain markdown for an editor's
// chat: the wordmark and tagline, then the build facts and the model the
// session runs on. It holds harness facts only (the build stamp, two
// identifiers and the mode), never a path, a prompt or model output.
func headerText(provider, model string) string {
	var b strings.Builder
	b.WriteString("**belai** · " + tagline + "\n\n")
	var facts []string
	known := func(s string) bool { return s != "" && s != "dev" && s != "unknown" }
	if known(version.Version) {
		facts = append(facts, "v"+strings.TrimPrefix(version.Version, "v"))
	}
	if known(version.Commit) {
		facts = append(facts, "commit "+sanitize.Ident(version.Commit, 12))
	}
	if known(version.BuildDate) {
		facts = append(facts, "built "+sanitize.Line(version.BuildDate, 32))
	}
	if len(facts) > 0 {
		goVersion, _, _ := strings.Cut(runtime.Version(), "-X:")
		facts = append(facts, goVersion+" "+runtime.GOOS+"/"+runtime.GOARCH)
	}
	if provider != "" {
		m := sanitize.Ident(provider, 48)
		if model != "" {
			m += " · " + sanitize.Line(model, 80)
		}
		facts = append(facts, m)
	}
	if len(facts) > 0 {
		b.WriteString(strings.Join(facts, " · ") + "\n\n")
	}
	return b.String()
}
