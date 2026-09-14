package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// GlobalDir returns the global Signet state directory (~/.signet).
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".signet"), nil
}

// GlobalSettingsPath returns ~/.signet/settings.json.
func GlobalSettingsPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// GlobalStatePath returns ~/.signet/state.json.
func GlobalStatePath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// ProjectDir returns the project-local state directory for a working directory.
func ProjectDir(workdir string) string {
	return filepath.Join(workdir, ".vulnetix")
}

// ProjectSettingsPath returns <workdir>/.vulnetix/settings.json.
func ProjectSettingsPath(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "settings.json")
}

// ProjectPlansDir returns <workdir>/.vulnetix/plans.
func ProjectPlansDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "plans")
}

// ProjectGoalsDir returns <workdir>/.vulnetix/goals.
func ProjectGoalsDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "goals")
}
