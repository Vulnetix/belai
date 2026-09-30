package tools

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/kanban"
)

// Handoff routing by clarity (kanban.quality.list and kanban.survey.list set
// to auto). A clear, concise task can go straight to the backlog for a
// builder; one that needs a person to confirm what is meant, or to split it,
// waits in review. The decision is deterministic and reads only facts the
// harness can count. It can send a card to review, never the other way: no
// score and no model's word moves a card out of the human gate, and a model's
// own doubt (clarity) only ever narrows.
const (
	// ClearTitleRunes is the longest title of a clear task.
	ClearTitleRunes = 100
	// ClearBodyMin and ClearBodyMax bound a clear task's body in bytes: enough
	// for a fresh agent to act on, not a sprawling brief.
	ClearBodyMin = 80
	ClearBodyMax = 2000
	// ClearMaxGates is the most gates a clear task carries; more says the task
	// hides several outcomes and should be split.
	ClearMaxGates = 5
)

// The clarity a model may declare for its own handoff.
const (
	ClarityClear   = "clear"
	ClarityUnclear = "needs_clarification"
	ClaritySplit   = "needs_split"
)

// routeByClarity returns the list a handoff goes to and, for review, the
// harness-composed reasons. It goes to backlog only when every check holds.
func routeByClarity(in kanban.ItemInput, declared string) (kanban.List, []string) {
	var why []string
	if utf8.RuneCountInString(in.Title) > ClearTitleRunes {
		why = append(why, "title over "+strconv.Itoa(ClearTitleRunes)+" characters")
	}
	switch n := len(strings.TrimSpace(in.Body)); {
	case n < ClearBodyMin:
		why = append(why, "body under "+strconv.Itoa(ClearBodyMin)+" characters")
	case n > ClearBodyMax:
		why = append(why, "body over "+strconv.Itoa(ClearBodyMax)+" characters")
	}
	runnable := 0
	for _, g := range in.Gates {
		if g.Kind == kanban.GateRunnable {
			runnable++
		}
	}
	if runnable == 0 {
		why = append(why, "no runnable gate the harness can check")
	}
	if len(in.Gates) > ClearMaxGates {
		why = append(why, "more than "+strconv.Itoa(ClearMaxGates)+" gates, so it may hide several tasks")
	}
	switch declared {
	case ClarityUnclear:
		why = append(why, "the agent asked for a person to clarify it")
	case ClaritySplit:
		why = append(why, "the agent asked for a person to split it")
	}
	if len(why) > 0 {
		return kanban.Review, why
	}
	return kanban.Backlog, nil
}
