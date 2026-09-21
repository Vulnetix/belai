package components

import "hash/fnv"

// FirstRunTip is the fixed hint a brand-new install shows instead of a rotating
// tip: the harness defaults to OpenRouter's free router, which needs an account
// and the signup credit before it will answer. It is deliberately outside the
// tips table — a first user must see it, not draw it.
const FirstRunTip = "new here? sign up at https://openrouter.ai/ — the signup credit runs the default openrouter/free model"

// tips is the rotating one-line hint table, drawn from facts that otherwise
// only exist in /help. Each entry is one line, so the banner's six-row height
// invariant is never at risk, and each names a real binding or command.
var tips = []string{
	"ctrl+x copies the session id",
	"shift+tab cycles mode: agent, plan, goal",
	"ctrl+o expands truncated output",
	"/resume returns to an earlier session",
	"f3 toggles guardrails",
	"f9 opens the runs panel",
	"esc esc clears the composer",
	"f2 toggles the caveman voice rewrite",
	"f4 toggles the permission ask gate",
	"f6 cycles reasoning effort",
	"f10 toggles the Vulnetix AI Firewall",
	"ctrl+r cycles reasoning display: auto, on, off",
	"ctrl+t cycles tool-call display: auto, on, off",
	"@ opens the file chooser in any mode",
	"!cmd runs a shell command; !!cmd runs it as a tracked process",
	"up browses prompt history; f7 saves the prompt to the library",
	"ctrl+s saves the hovered panel to a file",
	"/model picks the provider and model for each role",
	"/providers manages providers, credentials and local models",
	"/add-dir widens the workspace to another directory",
	"ctrl+l clears the transcript view; the session is kept",
	"/compact summarises the session into a new one",
	"drag selects text; releasing copies it",
	"ctrl+d exits — press twice; esc cancels",
	"f8 opens the runs panel on the subagents tab",
	"/agent picks a profile or runs a background agent",
	"/permissions edits the tool permission rules",
	"/prompts manages the prompt library",
	"/yolo toggles guardrails and the ask gate together",
	"ctrl+c copies the prompt, or the hovered panel",
	"enter while a turn runs steers it instead of queueing a new one",
	"/vulnetix runs a code review and manages the firewall",
}

// PickTip returns a stable tip for the given seed string. It must be chosen
// once, not per render — the banner re-renders every frame, so a per-render
// pick would strobe. An empty seed returns "" so the caller can fall back to
// the default /help line.
func PickTip(seed string) string {
	if seed == "" {
		return ""
	}
	return tips[tipOffset(seed)%len(tips)]
}

// CycleTip returns the tip step places after the seed's own tip, wrapping. It
// is what a rotating surface (the composer's working line) calls with a step
// derived from elapsed time, so two sessions started at once do not show the
// same hint and one session never repeats until the table is exhausted.
func CycleTip(seed string, step int) string {
	if step < 0 {
		step = -step
	}
	return tips[(tipOffset(seed)+step)%len(tips)]
}

// TipCount is the number of rotating tips, exported for tests that assert the
// rotation covers the table.
func TipCount() int { return len(tips) }

// tipOffset hashes a seed into a table offset.
func tipOffset(seed string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return int(h.Sum32() % uint32(len(tips)))
}
