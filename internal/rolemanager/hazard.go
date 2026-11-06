package rolemanager

import (
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// RecordHazard emits one step of a hazard-rail ask (docs/hazard-rails.md) to the
// activity feed: verdict "asked" (put to the user), "approved" or "denied" (the
// user's answer) or "withheld" (nobody could be asked). ids are the closed
// hazard identifiers joined by "+". Only the tool name and the number of
// identifiers are recorded; the command and the path never are.
func RecordHazard(verdict, tool, ids string) {
	n := 0
	if ids != "" {
		n = strings.Count(ids, "+") + 1
	}
	recordTimed(EventHazardAsk, verdict, sanitize.Ident(tool, 64), "hazards="+strconv.Itoa(n), 0, "", 0)
}

func hazardAskDescription(a Activity) Description {
	n := field(a.Detail, "hazards")
	if n == "" || !isDigits(n) {
		n = "a"
	}
	tool := strings.TrimSpace(a.Subject)
	if tool == "" {
		tool = "A tool"
	}
	d := Description{
		Summary: tool + " call matched " + n + " hazard rail(s), which always ask",
		Tone:    ToneCaution,
		Levels:  LevelDecisions,
	}
	switch a.Verdict {
	case "asked":
		d.Outcome = "asked you, whatever the rules say"
	case "approved":
		d.Outcome = "you approved it once"
		d.Tone = ToneClear
	case "denied":
		d.Outcome = "you refused it"
		d.Tone = ToneBlocked
	case "withheld":
		d.Outcome = "nobody could be asked, so it did not run"
		d.Tone = ToneBlocked
	default:
		d.Outcome = "handled"
	}
	return d
}
