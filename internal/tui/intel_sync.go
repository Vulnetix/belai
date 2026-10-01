package tui

import (
	"time"

	"github.com/vulnetix/belai/internal/budget"
)

// intelSyncEvery is the least time between two intel_state entries. A call can
// move a limit by a percent many times a minute; the website redraws on the
// next poll anyway, so a snapshot a minute is as live as it can show.
const intelSyncEvery = time.Minute

// syncIntel writes the session intelligence into the session as an intel_state
// entry, so the website shows the same limits, pace and runway as the footer.
// It writes at most once a minute and only when the snapshot says something new
// (budget.IntelState.Signature); the entry is skipped on resume and export like
// the other state entries.
func (a *App) syncIntel(now time.Time) {
	if a.budgets == nil || a.store == nil || a.storeDisabled {
		return
	}
	if !a.intelSyncAt.IsZero() && now.Sub(a.intelSyncAt) < intelSyncEvery {
		return
	}
	state := budget.NewIntelState(a.budgets.Intel(now, a.cfg.Provider, a.cycleBudgets()))
	sig := state.Signature()
	if sig == a.intelSyncSig {
		return
	}
	a.intelSyncAt = now
	a.intelSyncSig = sig
	a.appendEntry(state.ToEntry(a.lastEntryID))
}
