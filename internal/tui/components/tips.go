package components

import "hash/fnv"

// tips is the rotating one-line shortcut table drawn from bindings that
// currently only exist in /help. Each entry is one line, so the banner's
// six-row height invariant is never at risk.
var tips = []string{
	"ctrl+x copies the session id",
	"shift+tab cycles mode",
	"ctrl+o expands truncated output",
	"/resume returns to an earlier session",
	"f3 toggles guardrails",
	"f9 opens the activity drawer",
	"esc esc clears the composer",
}

// PickTip returns a stable tip for the given seed string. It must be chosen
// once, not per render — the banner re-renders every frame, so a per-render
// pick would strobe. An empty seed returns "" so the caller can fall back to
// the default /help line.
func PickTip(seed string) string {
	if seed == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return tips[int(h.Sum32())%len(tips)]
}
