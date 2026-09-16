package modes

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tools"
)

// PlanOption is the user's choice after a plan is extracted.
type PlanOption string

const (
	PlanExecute PlanOption = "execute"
	PlanStay    PlanOption = "stay"
	PlanRefine  PlanOption = "refine"
)

// PlanRoute is the outcome of routing a plan option.
type PlanRoute struct {
	Mode   Mode
	Action string // execute, stay, refine
}

// RoutePlanOption maps a user selection to the resulting mode. Execute leaves
// plan mode (full tools restored); Stay and Refine remain in plan mode.
func RoutePlanOption(opt PlanOption) (PlanRoute, error) {
	switch opt {
	case PlanExecute:
		return PlanRoute{Mode: ModeAgent, Action: "execute"}, nil
	case PlanStay:
		return PlanRoute{Mode: ModePlan, Action: "stay"}, nil
	case PlanRefine:
		return PlanRoute{Mode: ModePlan, Action: "refine"}, nil
	default:
		return PlanRoute{}, fmt.Errorf("unknown plan option %q", opt)
	}
}

// Todo is one tracked plan step.
type Todo struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// PlanState is the persisted plan-mode state (enabled / executing / todos). It
// is stored as a session entry so it survives resume.
type PlanState struct {
	Enabled   bool   `json:"enabled"`
	Executing bool   `json:"executing"`
	Todos     []Todo `json:"todos"`
}

// ToEntry renders PlanState as a session entry.
func (s PlanState) ToEntry(parentID string) session.Entry {
	data, err := json.Marshal(s)
	if err != nil {
		data = []byte("{}")
	}
	return session.Entry{
		ID:       session.MustID(),
		ParentID: parentID,
		Type:     "plan_state",
		Content:  string(data),
	}
}

// PlanStateFromEntry parses a session entry back into PlanState.
func PlanStateFromEntry(e session.Entry) (PlanState, error) {
	var s PlanState
	if err := json.Unmarshal([]byte(e.Content), &s); err != nil {
		return PlanState{}, fmt.Errorf("parse plan state: %w", err)
	}
	return s, nil
}

// Progress builds a plans.Progress from the tracked todos.
func (s PlanState) Progress() *plans.Progress {
	p := plans.NewProgress(len(s.Todos))
	for _, t := range s.Todos {
		if t.Done {
			p.MarkDone(t.N)
		}
	}
	return p
}

// ApplyMarkers scans assistant text for [DONE:n] markers and writes completion
// back into s.Todos[i].Done, mutating the PlanState so the write is not
// discarded when Progress() is rebuilt on the next call.
//
// Pass only model-authored assistant text. A marker appearing in a tool result
// or a repository file must never advance the plan.
func (s *PlanState) ApplyMarkers(assistantText string) {
	for _, n := range plans.ParseDoneMarkers(assistantText) {
		for i := range s.Todos {
			if s.Todos[i].N == n {
				s.Todos[i].Done = true
			}
		}
	}
}

// writeTools are built-in edit/write tools disabled in plan mode.
var writeTools = map[string]bool{
	"write":         true,
	"edit":          true,
	"apply_patch":   true,
	"patch":         true,
	"create_file":   true,
	"delete_file":   true,
	"rename_file":   true,
	"move_file":     true,
	"copy_file":     true,
	"replace":       true,
	"remove":        true,
	"notebook_edit": true,
	"insert":        true,
}

// IsWriteTool reports whether a tool is a built-in edit/write tool. Tool
// names are case-folded so a registered "Bash" cannot bypass a lowercase key.
func IsWriteTool(name string) bool { return writeTools[strings.ToLower(name)] }

// BashAllowed reports whether a bash command is within the read-only allowlist.
// It forwards to internal/tools, the single source of truth; plan mode and the
// read-only Bash tool share one parser and one word list.
func BashAllowed(command string) bool { return tools.BashAllowed(command) }

// ToolAllowed reports whether a tool call may proceed. In plan mode write
// tools are disabled and bash is restricted to the read-only allowlist; other
// tools remain active. Names are case-folded.
func ToolAllowed(name string, args map[string]any, planEnabled bool) bool {
	if !planEnabled {
		return true
	}
	if IsWriteTool(name) {
		return false
	}
	if strings.EqualFold(name, "bash") {
		cmd, _ := args["command"].(string)
		return BashAllowed(cmd)
	}
	return true
}
