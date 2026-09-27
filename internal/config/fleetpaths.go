package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentsDir returns <GlobalDir>/agents, the fleet's state: run/ holds one
// record per worker, logs/ their logs, memory/ each profile's lessons.
func AgentsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agents"), nil
}

// WorktreesDir returns where fleet workers create their git worktrees:
// $BELAI_WORKTREES_DIR, else ~/.vulnetix/worktrees.
//
// It is never inside a repository (a worktree there would sit inside the
// main session's root and its .vulnetix project directory), and never under
// the global state directory, which the OS sandbox hides from every command
// — a worker's Bash could not see its own worktree.
func WorktreesDir() (string, error) {
	dir := os.Getenv("BELAI_WORKTREES_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home dir: %w", err)
		}
		dir = filepath.Join(home, ".vulnetix", "worktrees")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if gd, err := GlobalDir(); err == nil {
		if g, err := filepath.Abs(gd); err == nil && within(g, abs) {
			return "", errors.New("the worktrees directory must not be inside Belai's state directory: the OS sandbox hides it")
		}
	}
	return abs, nil
}

// within reports whether path is dir or below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
