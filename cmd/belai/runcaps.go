package main

import (
	"errors"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/config"
)

// exitRunCap is the exit code of a `-prompt` run that stopped on a run cap
// (docs/run-caps.md): distinct from a failure (1), a usage error (2) and an
// interrupt (130), so a script can tell a bounded run from a broken one.
const exitRunCap = 3

// errRunCapReached ends a headless run whose run cap was reached. The reply was
// already printed.
var errRunCapReached = errors.New("run cap reached: the run stopped before it finished (see -max-cost, -max-tokens, -max-wall and run_caps in settings)")

// applyRunCapFlags folds -max-cost, -max-tokens and -max-wall into the
// settings. A flag is the user's own choice, so it sets the cap whatever a
// settings file says, and an unset flag (zero) leaves the file's value.
// Malformed values reach config.ValidateRunCaps, which refuses to start.
func applyRunCapFlags(s *config.Settings, maxCost float64, maxTokens, maxWall int) {
	if maxCost == 0 && maxTokens == 0 && maxWall == 0 {
		return
	}
	rc := config.RunCapsSettings{}
	if s.RunCaps != nil {
		rc = *s.RunCaps
	}
	if maxCost != 0 {
		rc.MaxCostUSD = &maxCost
	}
	if maxTokens != 0 {
		rc.MaxTokens = &maxTokens
	}
	if maxWall != 0 {
		rc.MaxWallMinutes = &maxWall
	}
	s.RunCaps = &rc
}

// rcRunCaps is the run-cap meter of a remote-control session. Its settings came
// through config.Resolve, which refuses a malformed cap, so an error here is
// unreachable and leaves no meter. A remote session cannot extend a cap: the
// website's answer to the ask is a stop, so a reached cap ends the run.
func rcRunCaps(s config.Settings, sessionID string) *budget.Meter {
	m, err := budget.MeterFor(s, sessionID)
	if err != nil {
		return nil
	}
	return m
}
