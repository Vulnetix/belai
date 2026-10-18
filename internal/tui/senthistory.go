package tui

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
)

// sentHistory is the conversation as the agent's provider last saw it, kept
// so the next prompt continues those exact bytes instead of a history rebuilt
// from the transcript. A rebuild drops the sealed directives, attachments and
// signed thinking of earlier turns and re-serialises tool arguments, so every
// request after a new prompt missed the provider's prompt cache from the
// previous prompt onward.
//
// It is used only while the transcript still says what it said when the turn
// ended: displayLen and displayFP fingerprint the rebuilt history at that
// moment, and any edit, rewind, resume or compaction since changes them, so
// the rebuild is used instead. It is tied to the session that produced it,
// whose nonce pool sealed its blocks.
type sentHistory struct {
	by         *agent.Session
	turns      []run.Turn
	displayLen int
	displayFP  [32]byte
}

// turnsFingerprint hashes the exported content of turns.
func turnsFingerprint(turns []run.Turn) [32]byte {
	b, _ := json.Marshal(turns)
	return sha256.Sum256(b)
}

// captureSentHistory records the session's sent history at the end of an
// agent-mode turn. Plan and goal turns run pass loops whose last call is not
// the whole conversation, so they keep the rebuild.
func (a *App) captureSentHistory(res run.Result) {
	a.sent = sentHistory{}
	if a.agent == nil || res.ModeDecision.Mode != modes.ModeAgent {
		return
	}
	turns := a.agent.SentHistory()
	if len(turns) == 0 {
		return
	}
	display := a.buildTurns()
	a.sent = sentHistory{by: a.agent, turns: turns, displayLen: len(display), displayFP: turnsFingerprint(display)}
}

// continueSentHistory returns the history for the next prompt: the recorded
// sent history plus whatever the transcript gained since, when the
// transcript before that point is unchanged and the same session will send
// it; otherwise display, the history rebuilt from the transcript.
func (a *App) continueSentHistory(display []run.Turn) []run.Turn {
	s := a.sent
	if s.by == nil || s.by != a.agent || len(display) < s.displayLen ||
		turnsFingerprint(display[:s.displayLen]) != s.displayFP {
		return display
	}
	return append(s.turns, display[s.displayLen:]...)
}
