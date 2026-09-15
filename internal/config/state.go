package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State persists runtime preferences across sessions.
type State struct {
	// Model is the last-selected (or saved-default) model ID.
	Model string `json:"model,omitempty"`
	// Provider is the last-selected provider name.
	Provider string `json:"provider,omitempty"`
	// Effort is the last-selected effort/thinking level.
	Effort string `json:"effort,omitempty"`
	// LastMode is the last active mode ("agent", "plan", or "goal").
	LastMode string `json:"last_mode,omitempty"`
	// ActivePlan is the currently selected plan name.
	ActivePlan string `json:"active_plan,omitempty"`
	// ActiveGoal is the currently selected goal name.
	ActiveGoal string `json:"active_goal,omitempty"`
	// ActiveProfile is the currently selected agent profile.
	ActiveProfile string `json:"active_profile,omitempty"`
}

// LoadState reads ~/.signet/state.json. A missing file yields zero-value
// state with no error.
func LoadState() (State, error) {
	path, err := GlobalStatePath()
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	return st, nil
}

// SaveState writes state to ~/.signet/state.json, creating directories as
// needed.
func SaveState(st State) error {
	path, err := GlobalStatePath()
	if err != nil {
		return err
	}
	mode := os.FileMode(0o700)
	if err := os.MkdirAll(filepath.Dir(path), mode); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write state %s: %w", path, err)
	}
	return nil
}
