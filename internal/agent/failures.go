package agent

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// A failed tool call is one of two things, and the loop treats them apart.
//
// A verdict is a decision of the harness: a classifier verdict on content, a
// permission or hook denial, a mode or scope refusal. Asking again cannot
// change it, so a streak of verdicts is stopped (withheldRepairDirective).
//
// A repairable failure is an error in the call itself: arguments that did not
// parse or validate, a tool that returned an error, a script that threw, a tool
// the model named in the wrong mode. The model can fix it, so the loop sends it
// back as a new turn to correct, bounded by repairCap so a model that cannot
// fix it does not spin.
//
// The visible strings are a contract with the TUI, ACP, pruning and
// telemetry, so a result is classified by its prefix here, in one place, and
// every producer of a repairable result uses the constants below.
const (
	withheldPrefix = "tool result withheld:"
	rejectedPrefix = tools.RejectedPrefix

	executionErrorPrefix = withheldPrefix + " execution error for"
	malformedArgsPrefix  = withheldPrefix + " malformed arguments for"
	truncatedArgsPrefix  = withheldPrefix + " arguments may be truncated"
)

// repairHint ends every repairable failure the harness words itself.
const repairHint = " Fix the call and issue it again."

// repairable reports whether a tool result is an error in the call the model
// can fix, as opposed to a verdict.
func repairable(result string) bool {
	for _, p := range []string{rejectedPrefix, executionErrorPrefix, malformedArgsPrefix, truncatedArgsPrefix} {
		if strings.HasPrefix(result, p) {
			return true
		}
	}
	return false
}

// verdict reports whether a tool result is a withheld verdict that retrying
// cannot change.
func verdict(result string) bool {
	return strings.HasPrefix(result, withheldPrefix) && !repairable(result)
}

// executionError words a tool's own error as a result the model can act on.
func executionError(name string, err error) string {
	return fmt.Sprintf("%s %q: %v.%s", executionErrorPrefix, name, err, repairHint)
}

// Repair caps: consecutive iterations in which every failure was repairable
// and nothing succeeded. Code mode gets one more because a script call is a
// whole program the model has to rewrite.
const (
	maxRepairIterations     = 3
	maxCodeRepairIterations = 4
	// maxMismatchFeedbacks bounds how often an unavailable tool name is fed
	// back before the abort policy ends the turn as it always did.
	maxMismatchFeedbacks = 2
)

// repairCap is the repair-streak bound for the current turn.
func (s *Session) repairCap() int {
	if s.turnCode {
		return maxCodeRepairIterations
	}
	return maxRepairIterations
}

// repairDirective is the sealed instruction after two repairable-only rounds:
// the errors are above, so it asks for corrected calls and names no plan. The
// failed calls ride as a note (their tool names and a short excerpt are the
// model's own call and the tool's own error, sanitised).
func repairDirective(failed []string) []run.Turn {
	body := "The last two rounds failed because of errors in your own tool calls, not because anything was refused. " +
		"Read each error above, correct the arguments (the argument names and shapes are in the tool definition, and a path is relative to the working directory or a session root), and issue the calls again now. " +
		"Do not answer without the tool calls the work still needs."
	return directiveTurnsWithNote(body, strings.Join(failed, "\n"))
}

// failedNote is one line of a repair directive's note: the tool and a short,
// sanitised excerpt of its error.
func failedNote(tool, result string) string {
	return fmt.Sprintf("%s: %s", sanitize.Line(tool, 40), sanitize.Line(result, 160))
}

// mismatchResults answers every call of a response that named a tool the
// model was not offered. The abort policy still ends the turn once the
// feedback has been refused maxMismatchFeedbacks times; before that the model
// gets the error as results it can read, with the closest offered name.
func mismatchResults(calls []rolemanager.ToolCall, offered []string) map[string]string {
	known := make(map[string]bool, len(offered))
	for _, n := range offered {
		known[n] = true
	}
	out := make(map[string]string, len(calls))
	for _, c := range calls {
		if known[c.Name] {
			out[c.ID] = rejectedPrefix + " not run, because another call in this response named a tool that is not offered. Issue it again with the offered tools."
			continue
		}
		msg := fmt.Sprintf("%s %q is not a tool you were offered.", rejectedPrefix, sanitize.Line(c.Name, 60))
		if closest := rolemanager.ClosestTool(c.Name, offered); closest != "" {
			msg += fmt.Sprintf(" The closest offered tool is %s.", closest)
		}
		out[c.ID] = msg + " Use only the tools in your tool list."
	}
	return out
}

// repairContinuationDirective opens the continuation after a pass that was
// stopped by repairCap: the errors are in the conversation, so it asks for the
// corrected calls and for the work to be finished, not for a wrap-up.
const repairContinuationDirective = "The last round ended because your tool calls kept failing on errors in the calls themselves; the errors are above. Correct the arguments and issue the calls again, then finish the work."
