package rolemanager

import (
	"fmt"
	"time"
)

// Session-record entries for the delivery crew's Jev relevance jobs. Each
// records a verdict word and rounded numbers only: never a task, a gate title,
// a clause or a score's input.

// RecordHandoffClarity emits the outcome of rating a handoff: verdict "clear"
// (no objection), "unclear" (sent to review) or "unknown" (the backend could
// not settle it, so the counted rule stood alone).
func RecordHandoffClarity(verdict string, scorePct int, model string, took time.Duration) {
	recordTimed(EventHandoffClarity, verdict, "", fmt.Sprintf("score=%d", scorePct), 0, model, took)
}

func handoffClarityDescription(a Activity) Description {
	d := Description{
		Summary: "Rated whether a handed-off task is clear enough to start",
		Outcome: "could not settle it, so only the counted rule decided where it went",
		Tone:    ToneNeutral,
		Levels:  LevelAll,
	}
	switch a.Verdict {
	case "clear":
		d.Outcome = "no objection, so the counted rule decided where it went"
		d.Levels = LevelDecisions
	case "unclear":
		d.Outcome = "rated unclear, so it waits in review for a person"
		d.Tone = ToneClear
		d.Levels = LevelDecisions
	}
	return d
}

// RecordGateAlignment emits the outcome of rating a card's runnable gates:
// verdict "flagged" (some gate may not measure its title), "aligned" or
// "unknown".
func RecordGateAlignment(verdict string, flagged, total int, model string, took time.Duration) {
	recordTimed(EventGateAlignment, verdict, "", fmt.Sprintf("flagged=%d of=%d", flagged, total), 0, model, took)
}

func gateAlignmentDescription(a Activity) Description {
	d := Description{
		Summary: "Rated whether a card's test gates show what their titles claim",
		Outcome: "could not settle it, so the gates stood as filed",
		Tone:    ToneNeutral,
		Levels:  LevelAll,
	}
	switch a.Verdict {
	case "aligned":
		d.Outcome = "each gate looks like it measures its title"
		d.Levels = LevelDecisions
	case "flagged":
		d.Outcome = "a gate may not measure its title, so the card waits in review"
		d.Tone = ToneClear
		d.Levels = LevelDecisions
	}
	return d
}

// RecordRequestCoverage emits the outcome of rating the tasks that cover a
// request's clauses: verdict "suspect" (a clause's tasks do not seem to do
// it), "sound" or "unknown".
func RecordRequestCoverage(verdict string, suspect, total int, model string, took time.Duration) {
	recordTimed(EventRequestCoverage, verdict, "", fmt.Sprintf("suspect=%d of=%d", suspect, total), 0, model, took)
}

func requestCoverageDescription(a Activity) Description {
	d := Description{
		Summary: "Rated whether the tasks covering a request do what it asks",
		Outcome: "could not settle it, so only the recorded coverage stood",
		Tone:    ToneNeutral,
		Levels:  LevelAll,
	}
	switch a.Verdict {
	case "sound":
		d.Outcome = "each covered part looks like its tasks do it"
		d.Levels = LevelDecisions
	case "suspect":
		d.Outcome = "a covered part may not be done by its tasks, so a gap card was filed"
		d.Tone = ToneClear
		d.Levels = LevelDecisions
	}
	return d
}
