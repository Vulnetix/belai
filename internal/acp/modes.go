package acp

import (
	"encoding/json"

	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/modes"
)

// modeAuto lets Belai pick the mode for each prompt, as it does without an
// editor. The others are the user's explicit choice in the editor and engage
// that mode through the same TurnInput.ForceMode a TUI mode cycle uses, so the
// mode's own tool surface and gates apply (plan mode still has no Bash).
const modeAuto = "auto"

var modeList = []map[string]any{
	{"id": modeAuto, "name": "Auto", "description": "Belai picks agent, plan or goal for each prompt"},
	{"id": string(modes.ModeAgent), "name": "Agent", "description": "Work on the task with the full tool surface"},
	{"id": string(modes.ModePlan), "name": "Plan", "description": "Read only: explore and write a plan, no edits and no shell"},
	{"id": string(modes.ModeGoal), "name": "Goal", "description": "Pursue the goal in passes until it is met"},
	{"id": string(modes.ModeCode), "name": "Code", "description": "Work with the full tool surface and batch many tool calls in one script"},
}

// modeState is the session/new answer's mode block.
func modeState(current string) map[string]any {
	avail := make([]any, len(modeList))
	for i, m := range modeList {
		avail[i] = m
	}
	return map[string]any{"currentModeId": current, "availableModes": avail}
}

// forcedMode maps an editor mode id to the mode a turn is forced into; auto is
// the empty mode, which classifies as usual.
func forcedMode(id string) modes.Mode {
	if id == modeAuto {
		return ""
	}
	return modes.Mode(id)
}

func knownMode(id string) bool {
	for _, m := range modeList {
		if m["id"] == id {
			return true
		}
	}
	return false
}

// setMode answers session/set_mode. The editor's choice is the user's, so it
// may leave any mode; an unknown id is refused.
func (s *Server) setMode(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
	}
	ss := s.lookup(p.SessionID)
	if ss == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown session %q", p.SessionID)
	}
	if !knownMode(p.ModeID) {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown mode %q", p.ModeID)
	}
	ss.mu.Lock()
	ss.mode = p.ModeID
	ss.mu.Unlock()
	s.update(ss, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.ModeID})
	return map[string]any{}, nil
}
