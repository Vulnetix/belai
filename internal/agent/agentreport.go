package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/run"
)

// Every mode ends on a report. Goal mode and an approved plan already do
// (goalReport); plan mode's deliverable is the plan. Agent and code mode used
// to end on whatever the last reply was, which after a long tool run is often
// a fragment or empty. agentFinish closes that: when the turn did work and its
// closing reply is not a report, or the turn was cut off at its budget or left
// todos open, the model is asked once, without tools, for the report.

// agentReportDirective is the sealed instruction of that turn.
const agentReportDirective = "The turn is ending. Do not call any tools. Write the final report for the user now: what you did and changed (each file and the substance of the change), how you verified it (the commands run and their outcome), and anything you did not finish or could not do, and why. If todos remain open, say which and why. Be concise and factual, and report only work that is visible in this conversation."

// agentReportMinLen is the length under which a closing reply is treated as
// not being a report. A short, accurate account of a small change is still a
// report; an empty or one-line fragment after a long run of edits is not.
const agentReportMinLen = 40

// wantsAgentReport decides whether the turn needs the report turn.
func (s *Session) wantsAgentReport(ctx context.Context, res run.Result, capped bool) bool {
	if ctx.Err() != nil || res.Clarify != nil {
		return false
	}
	if !s.allowPassLoop || s.planMode || s.exploreSubagent || s.reportOnly || s.kanbanWrapUpPass {
		return false
	}
	// A turn cut off at its budget, or one that left a todo open, always says
	// where it stopped.
	if capped || s.turnTodos.HasOpen() {
		return true
	}
	// A turn that changed no file answered a question or looked something up:
	// its reply is the answer, however short. Only a turn that changed
	// something owes an account of it.
	if s.turnMutations == 0 {
		return false
	}
	return len(strings.TrimSpace(res.Reply)) < agentReportMinLen
}

// agentFinish ends an agent or code turn: it publishes the turn's todo list,
// then writes the final report when the turn needs one. The report is best
// effort and never costs the turn: a failed or empty report leaves the reply as
// it was, and a turn that has no reply at all gets a harness-composed line of
// facts, so a session never ends silent.
func (s *Session) agentFinish(ctx context.Context, system string, streaming bool, emit func(Event), res run.Result, capped bool) run.Result {
	s.flushTodos(emit)
	if !s.wantsAgentReport(ctx, res, capped) {
		return res
	}
	var note string
	if s.turnHasTodos {
		note = todoNote(s.turnTodos, true)
	}
	instruction := directiveTurnsWithNote(agentReportDirective, note)
	reported := s.finalReport(ctx, system, withReply(s.lastTurns, res.Reply), streaming, emit, "", instruction, res)
	if strings.TrimSpace(reported.Reply) == "" && ctx.Err() == nil {
		reported.Reply = s.harnessReport()
	}
	return reported
}

// harnessReport is the fallback closing line: facts the harness counted, never
// model text.
func (s *Session) harnessReport() string {
	msg := fmt.Sprintf("The turn ended without a written report after %d tool call(s).", s.turnToolRuns)
	if s.turnHasTodos {
		total := len(s.turnTodos.Items)
		open := len(s.turnTodos.Open())
		msg += fmt.Sprintf(" Todos: %d of %d done.", total-open, total)
	}
	return msg
}
