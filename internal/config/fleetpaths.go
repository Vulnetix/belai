package config

import (
	"crypto/sha256"
	"encoding/hex"
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

// KnowledgeDir returns <GlobalDir>/knowledge, where the retrieval indexes of
// agent profiles and projects are kept (docs/knowledge.md). It holds document
// text, so like the rest of the state directory the OS sandbox hides it from
// every command.
func KnowledgeDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "knowledge"), nil
}

// ProfileKnowledgeDir is the directory of one agent profile's index, keyed by
// the profile's id (a lowercase UUID) because a profile can be renamed.
func ProfileKnowledgeDir(profileID string) (string, error) {
	if !validKnowledgeID(profileID) {
		return "", fmt.Errorf("%q is not a profile id", profileID)
	}
	dir, err := KnowledgeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "profiles", profileID), nil
}

// ProjectKnowledgeDir is the directory of one project's index, keyed by the
// SHA-256 of the project's cleaned absolute root so the path names no
// directory. root is the trusted repository root, never a worktree.
func ProjectKnowledgeDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	dir, err := KnowledgeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects", hex.EncodeToString(sum[:])), nil
}

func validKnowledgeID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'):
		default:
			return false
		}
	}
	return true
}
