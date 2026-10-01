package kanban

import (
	"slices"
	"strconv"

	"github.com/vulnetix/belai/internal/audit"
)

// The board's audit facts (internal/audit, docs/audit.md). The store records
// what the harness did to a card on its own: a lapsed lease, a finding filed or
// found gone. What a worker did (a claim, a release, a commit) is recorded by
// the worker, which knows its profile, crew and repository. Every value is a
// harness fact (an id, a list, a count); card text is never read here. Each is
// recorded after the write succeeds, so the log never names a change the board
// did not keep, and audit.Emit does nothing when sync is off.

// VulnID returns the advisory or rule id the card was filed for when it is a
// security card, and "" for any other card. Quality and gate cards also carry a
// Finding, but they are not vulnerability work and must not link to one. A card
// is a security card when the security sweep labelled it (vuln, or gone and
// needs-verify once a reconcile found it fixed) or a verifier has recorded a
// verdict or VEX on it.
func (it Item) VulnID() string {
	if it.Finding == "" {
		return ""
	}
	if slices.Contains(it.Labels, LabelVuln) || slices.Contains(it.Labels, LabelGone) ||
		slices.Contains(it.Labels, LabelNeedsVerify) || it.Verdict != "" || it.VEX != "" {
		return it.Finding
	}
	return ""
}

// auditCard records a fact about a card.
func auditCard(kind audit.Kind, it Item, f audit.Fact) {
	if !audit.Enabled() {
		return
	}
	f.Kind, f.ItemID, f.Repo = kind, it.ID, it.Project
	if f.VulnID == "" {
		f.VulnID = it.VulnID()
	}
	if f.VulnID != "" && f.SeenRef == "" {
		f.SeenRef = it.SeenRef
	}
	audit.Emit(f)
}

// auditLapsed records each claim the harness reaped because its lease ran out.
// The items are copies taken before the release, so the claim is still on them.
func auditLapsed(lapsed []Item) {
	for _, it := range lapsed {
		auditCard(audit.CardLeaseLapse, it, audit.Fact{
			ActorKind: audit.ActorHarness, Outcome: "lapsed",
			Data: map[string]string{"worker": it.ClaimedBy, "list": string(it.claimSource()), "attempt": strconv.Itoa(it.Attempts + 1)},
		})
	}
}

// auditFinding records a finding card the sweep filed or found gone. change is
// the word for what happened (created, reopened, gone, closed).
func auditFinding(kind audit.Kind, it Item, change string) {
	auditCard(kind, it, audit.Fact{
		ActorKind: audit.ActorHarness, Outcome: change, Verdict: string(it.Verdict),
		Data: map[string]string{"change": change},
	})
}
