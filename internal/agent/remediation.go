package agent

import (
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/vulnid"
)

// Remediation turns. A request to fix an advisory in this repository (the
// prepared remediation prompt, "fix GHSA-…", or the link and vdb command the
// vulnerability row copies) has one deliverable: the edit that fixes it. The
// harness recognises it from the sanitised prompt alone (vulnid.IsRemediation,
// no model), so it is never a question for the mode-choice panel, never goes
// to a read-only profile, and carries the contract below as a sealed directive.
//
// Sessions showed these turns reading for minutes: a lookup that already named
// the fixed version, then board searches, reads of the harness's own source and
// re-verification, and a closing report with no change. The lookup data is
// enough to act on, so the directive orders the work (lookup, read the manifest,
// edit) and the pass loop pushes after three rounds without a change instead of
// eight, and once more if the model tries to finish with none. A turn may end
// without an edit only on proof that nothing needs one.
const (
	// remediationNudgeAfter is how many tool rounds in a row may change no file
	// on a remediation turn before it is told to edit.
	remediationNudgeAfter = 3
	// remediationGuards is how many times a remediation turn's final answer is
	// refused for carrying no edit.
	remediationGuards = 1

	remediationDirective = "Remediation turn: the deliverable is the file change that fixes the advisory in this repository. " +
		"Work in this order. First, one lookup per identifier with the Vulnetix tool or the Vulnetix MCP server; its fixed versions and remediation are enough to act on, so do not re-verify them, search the web for them or look them up again. " +
		"Second, read the manifest and lockfile that name the affected package. " +
		"Third, edit the manifest to the fixed version, update the lockfile the way the project's own tooling does, and run the project's checks. " +
		"Until the edit has landed do not search or move kanban cards, read the harness's own source or audit other advisories. " +
		"A turn may end with no change only when you quote the file and line that show the repository is not affected or already at the fixed version."

	remediationNudge = "You have spent several rounds researching a remediation without changing a file. The lookup already named the fixed version: edit the manifest to it now and update the lockfile with the project's own tooling, in your next response. " +
		"If the repository is not affected or already at the fixed version, quote the file and line that prove it and finish."

	remediationFinishGuard = "That reply ends the remediation with no file changed. Make the fix now: edit the manifest to the fixed version, update the lockfile with the project's own tooling and run the checks. " +
		"Finish without a change only if your reply quotes the file and line that prove the repository is not affected or already at the fixed version; a report that only describes what could be done is not the deliverable."
)

// remediationTurn reports whether the sanitised prompt is a remediation request.
func remediationTurn(prompt string) bool { return vulnid.IsRemediation(prompt) }

// refuseRemediationFinish reports whether a final answer with no tool call
// should be sent back for the edit: a remediation turn in agent mode whose pass
// changed no file, with a refusal left. Goal mode has its own no-write
// escalation, and a turn that cannot edit never reaches here latched.
func (s *Session) refuseRemediationFinish(mutations int, mode modes.Mode) bool {
	if !s.turnRemediation || mode != modes.ModeAgent || mutations > 0 || s.remediationRefused >= remediationGuards {
		return false
	}
	if s.planMode || s.turnReadOnly || s.exploreSubagent || s.reportOnly || s.kanbanWrapUpPass {
		return false
	}
	s.remediationRefused++
	return true
}
