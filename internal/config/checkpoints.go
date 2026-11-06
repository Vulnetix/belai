package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckpointSettings configures file checkpoints (docs/checkpoints.md): before
// a mutating call changes a file the harness keeps a bounded copy of what the
// file held, so /undo and /rewind can put it back.
//
// The store lives in the user's own state directory and holds copies of the
// user's files, so every limit and the retention belong to the user. The
// project layer, which a cloned repository controls, may only switch the
// feature off: lowering a limit or the retention would delete the history the
// user already has.
type CheckpointSettings struct {
	// Enabled switches checkpoints. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxPerSession is how many checkpoints one session keeps before the
	// oldest are dropped. Default 50, 1 to 500.
	MaxPerSession *int `json:"max_per_session,omitempty"`
	// MaxBytes bounds the file copies one session keeps. Default 200 MiB,
	// 1 MiB to 10 GiB.
	MaxBytes *int64 `json:"max_bytes,omitempty"`
	// RetainDays is how long a session's checkpoints are kept after its last
	// change. Default 7, 1 to 365.
	RetainDays *int `json:"retain_days,omitempty"`
}

// Checkpoint defaults and bounds.
const (
	DefaultCheckpointsMaxPerSession = 50
	DefaultCheckpointsMaxBytes      = int64(200) << 20
	DefaultCheckpointsRetainDays    = 7

	maxCheckpointsPerSession = 500
	minCheckpointsBytes      = int64(1) << 20
	maxCheckpointsBytes      = int64(10) << 30
	maxCheckpointsRetainDays = 365
)

// CheckpointsEnabled reports whether file checkpoints are kept. Default on.
func (s Settings) CheckpointsEnabled() bool {
	return s.Checkpoints == nil || s.Checkpoints.Enabled == nil || *s.Checkpoints.Enabled
}

// CheckpointsMaxPerSession is the most checkpoints one session keeps. An
// out-of-range value falls back to the default.
func (s Settings) CheckpointsMaxPerSession() int {
	if c := s.Checkpoints; c != nil && c.MaxPerSession != nil && *c.MaxPerSession >= 1 && *c.MaxPerSession <= maxCheckpointsPerSession {
		return *c.MaxPerSession
	}
	return DefaultCheckpointsMaxPerSession
}

// CheckpointsMaxBytes is the most file bytes one session keeps.
func (s Settings) CheckpointsMaxBytes() int64 {
	if c := s.Checkpoints; c != nil && c.MaxBytes != nil && *c.MaxBytes >= minCheckpointsBytes && *c.MaxBytes <= maxCheckpointsBytes {
		return *c.MaxBytes
	}
	return DefaultCheckpointsMaxBytes
}

// CheckpointsRetainDays is how many days an idle session's checkpoints stay.
func (s Settings) CheckpointsRetainDays() int {
	if c := s.Checkpoints; c != nil && c.RetainDays != nil && *c.RetainDays >= 1 && *c.RetainDays <= maxCheckpointsRetainDays {
		return *c.RetainDays
	}
	return DefaultCheckpointsRetainDays
}

// ValidateCheckpoints rejects a checkpoints key outside its range, naming the
// key. A repository's value never gets here: mergeCheckpoints ignores it.
func ValidateCheckpoints(s Settings) error {
	c := s.Checkpoints
	if c == nil {
		return nil
	}
	if p := c.MaxPerSession; p != nil && (*p < 1 || *p > maxCheckpointsPerSession) {
		return fmt.Errorf("checkpoints.max_per_session %d must be between 1 and %d", *p, maxCheckpointsPerSession)
	}
	if p := c.MaxBytes; p != nil && (*p < minCheckpointsBytes || *p > maxCheckpointsBytes) {
		return fmt.Errorf("checkpoints.max_bytes %d must be between %d and %d", *p, minCheckpointsBytes, maxCheckpointsBytes)
	}
	if p := c.RetainDays; p != nil && (*p < 1 || *p > maxCheckpointsRetainDays) {
		return fmt.Errorf("checkpoints.retain_days %d must be between 1 and %d", *p, maxCheckpointsRetainDays)
	}
	return nil
}

// mergeCheckpoints folds in over cur. A user layer or a flag sets any key in
// range. The project layer only switches the feature off; every other key it
// carries, and an enabled of true, is ignored. An out-of-range value from any
// layer is dropped.
func mergeCheckpoints(cur, in *CheckpointSettings, project bool) *CheckpointSettings {
	if in == nil {
		return cur
	}
	out := &CheckpointSettings{}
	if cur != nil {
		*out = *cur
	}
	if in.Enabled != nil && !(project && *in.Enabled) {
		v := *in.Enabled
		out.Enabled = &v
	}
	if project {
		if cur == nil && out.Enabled == nil {
			return nil
		}
		return out
	}
	if p := in.MaxPerSession; p != nil && *p >= 1 && *p <= maxCheckpointsPerSession {
		v := *p
		out.MaxPerSession = &v
	}
	if p := in.MaxBytes; p != nil && *p >= minCheckpointsBytes && *p <= maxCheckpointsBytes {
		v := *p
		out.MaxBytes = &v
	}
	if p := in.RetainDays; p != nil && *p >= 1 && *p <= maxCheckpointsRetainDays {
		v := *p
		out.RetainDays = &v
	}
	return out
}

// checkpointsIgnored names the project keys mergeCheckpoints would not apply on
// top of user, for the note Resolve prints.
func checkpointsIgnored(proj *CheckpointSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	if proj.Enabled != nil && *proj.Enabled {
		out = append(out, "checkpoints.enabled")
	}
	if proj.MaxPerSession != nil {
		out = append(out, "checkpoints.max_per_session")
	}
	if proj.MaxBytes != nil {
		out = append(out, "checkpoints.max_bytes")
	}
	if proj.RetainDays != nil {
		out = append(out, "checkpoints.retain_days")
	}
	return out
}

// CheckpointsRoot returns <GlobalDir>/checkpoints, the parent of every
// session's checkpoint store. It is part of the state directory, so the OS
// sandbox hides it, and the file tools refuse a path under it.
func CheckpointsRoot() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "checkpoints"), nil
}

// CheckpointsDir returns the store of one session in one working directory:
// <GlobalDir>/checkpoints/<WorkdirKey(workdir)>/<sessionID>. Callers create it
// with mode 0700.
func CheckpointsDir(workdir, sessionID string) (string, error) {
	root, err := CheckpointsRoot()
	if err != nil {
		return "", err
	}
	if sessionID == "" || sessionID != filepath.Base(sessionID) || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("checkpoints: %q is not a session id", sessionID)
	}
	abs := workdir
	if a, err := filepath.Abs(workdir); err == nil {
		abs = a
	}
	return filepath.Join(root, WorkdirKey(abs), sessionID), nil
}

// UnderCheckpoints reports whether an absolute path lies in the checkpoint
// store, after resolving symlinks in the part that exists. The file tools use
// it to refuse the store.
func UnderCheckpoints(abs string) bool {
	root, err := CheckpointsRoot()
	if err != nil {
		return false
	}
	return within(root, abs) || within(evalExisting(root), evalExisting(abs))
}

// evalExisting resolves symlinks in the longest existing prefix of p.
func evalExisting(p string) string {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, tail...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		if _, err := os.Lstat(cur); err == nil {
			return p
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}
