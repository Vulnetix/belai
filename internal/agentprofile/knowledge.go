package agentprofile

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Knowledge limits.
const (
	// MaxKnowledgePaths is the most paths a profile may list. It equals
	// knowledge.MaxProfilePaths, which a test pins.
	MaxKnowledgePaths = 32
	// maxKnowledgePathLen bounds one listed path.
	maxKnowledgePathLen = 1024
)

// KnowledgeSpec lists the documents a profile may search (docs/knowledge.md).
// Each path is a file or a directory on the host, written by the user in the
// profile file. The harness reads them itself, indexes them and lets the
// agent search the result through Grep, Glob and Read; the agent is never
// given a path to read.
//
// The field is local to the host. A profile installed from the website cannot
// carry it, and a backup leaves it out, because a remote request must never be
// able to choose which local files get indexed.
type KnowledgeSpec struct {
	// Paths are absolute, or start with "~/".
	Paths []string `json:"paths"`
}

// validateKnowledge checks the knowledge block.
func (p AgentProfile) validateKnowledge() error {
	k := p.Knowledge
	if k == nil {
		return nil
	}
	if len(k.Paths) == 0 {
		return errors.New("knowledge.paths must list at least one path")
	}
	if len(k.Paths) > MaxKnowledgePaths {
		return fmt.Errorf("knowledge.paths lists %d paths; the most is %d", len(k.Paths), MaxKnowledgePaths)
	}
	seen := map[string]bool{}
	for _, raw := range k.Paths {
		if err := validKnowledgePath(raw); err != nil {
			return fmt.Errorf("knowledge.paths: %w", err)
		}
		clean := filepath.Clean(raw)
		if seen[clean] {
			return fmt.Errorf("knowledge.paths lists %q twice", raw)
		}
		seen[clean] = true
	}
	return nil
}

func validKnowledgePath(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("a path is empty")
	case len(s) > maxKnowledgePathLen:
		return fmt.Errorf("a path is longer than %d bytes", maxKnowledgePathLen)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return errors.New("a path holds a control character")
		}
	}
	if !filepath.IsAbs(s) && !strings.HasPrefix(s, "~/") {
		return fmt.Errorf("%q must be an absolute path or start with ~/", s)
	}
	for _, seg := range strings.Split(filepath.ToSlash(s), "/") {
		if seg == ".." {
			return fmt.Errorf("%q must not contain ..", s)
		}
	}
	return nil
}

// KnowledgePaths returns the listed paths, or nil.
func (p AgentProfile) KnowledgePaths() []string {
	if p.Knowledge == nil {
		return nil
	}
	return append([]string(nil), p.Knowledge.Paths...)
}
