package agent

import (
	"strings"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

// hazardsFor returns the hazard identifiers of one call, or nil when the floor
// is off (permissions.hazard_ask false in a user layer) or the call holds none.
// The identifiers come from fixed tables (shellsafe.Hazards for Bash, the
// protected-path table for Write, Edit and apply_patch); nothing a model wrote
// is copied into them.
func (s *Session) hazardsFor(tool tools.Tool, args map[string]any) []string {
	if !s.settings.HazardAskOn() {
		return nil
	}
	return tools.HazardCall(tool, args, s.settings.ProtectedPathList())
}

// noteHazard records one step of a hazard ask: "asked" when it was put to the
// user, "approved" or "denied" with the answer, "withheld" when nobody could be
// asked. The role-manager row and the audit event hold identifiers and the
// tool name only, never the command or the path.
func (s *Session) noteHazard(verdict, tool string, ids []string) {
	joined := strings.Join(ids, "+")
	rolemanager.RecordHazard(verdict, tool, joined)
	switch verdict {
	case "asked":
		s.emitHazardAudit(audit.HazardAsked, verdict, tool, joined)
	case "denied", "withheld":
		s.emitHazardAudit(audit.HazardDenied, verdict, tool, joined)
	}
}

func (s *Session) emitHazardAudit(kind audit.Kind, outcome, tool, ids string) {
	audit.Emit(audit.Fact{
		Kind: kind, ActorKind: audit.ActorHarness, SessionID: s.sessionID, Outcome: outcome,
		Data: map[string]string{"id": ids, "tool": tool},
	})
}
