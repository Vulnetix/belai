package goals

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/session"
)

// GoalStatus is the lifecycle state of a running goal.
type GoalStatus string

const (
	StatusActive        GoalStatus = "active"
	StatusPaused        GoalStatus = "paused"
	StatusBudgetLimited GoalStatus = "budget_limited"
	StatusComplete      GoalStatus = "complete"
)

// GoalState is the run-time goal state persisted as a `goal_state` session
// entry: what the UI and a resumed session need to know about a goal in
// flight. It is a progress report, never an input to the pass loop's
// decisions.
type GoalState struct {
	Version         int    `json:"version"`               // 1
	ID              string `json:"id"`                    // session-entry id
	Objective       string `json:"objective"`             // goal text
	Status          string `json:"status"`                // active|paused|budget_limited|complete
	TokenBudget     *int   `json:"tokenBudget,omitempty"` // nil = unbounded
	TokensUsed      int    `json:"tokensUsed"`
	TimeUsedSeconds int    `json:"timeUsedSeconds"`
	Passes          int    `json:"passes"`
	CreatedAt       int64  `json:"createdAt"` // unix millis
	UpdatedAt       int64  `json:"updatedAt"` // unix millis
}

// EntryTypeGoalState is the session entry type carrying the run-time goal.
const EntryTypeGoalState = "goal_state"

// NewGoalState builds a fresh active goal state for an objective.
func NewGoalState(objective string) GoalState {
	now := time.Now().UnixMilli()
	return GoalState{
		Version:   1,
		ID:        session.MustID(),
		Objective: objective,
		Status:    string(StatusActive),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ToEntry renders GoalState as a session entry, copying the plan_state pattern.
func (s GoalState) ToEntry(parentID string) session.Entry {
	s.UpdatedAt = time.Now().UnixMilli()
	data, err := json.Marshal(s)
	if err != nil {
		data = []byte("{}")
	}
	return session.Entry{
		ID:       session.MustID(),
		ParentID: parentID,
		Type:     EntryTypeGoalState,
		Content:  string(data),
	}
}

// GoalStateFromEntry parses a session entry back into GoalState.
func GoalStateFromEntry(e session.Entry) (GoalState, error) {
	var s GoalState
	if err := json.Unmarshal([]byte(e.Content), &s); err != nil {
		return GoalState{}, fmt.Errorf("parse goal state: %w", err)
	}
	return s, nil
}

// LatestGoalState returns the last goal_state entry, or ok=false when none.
func LatestGoalState(entries []session.Entry) (GoalState, bool) {
	var out GoalState
	found := false
	for _, e := range entries {
		if e.Type != EntryTypeGoalState {
			continue
		}
		gs, err := GoalStateFromEntry(e)
		if err != nil {
			continue
		}
		out = gs
		found = true
	}
	return out, found
}
