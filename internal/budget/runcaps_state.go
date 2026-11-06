package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// A session's run-cap counters live in one small file under
// config.RunCapsDir, so a resumed session carries what it spent. The file holds
// the numbers of Snapshot and nothing else, is written atomically at 0600 in a
// 0700 directory, and a file that does not parse is used for nothing and left
// alone.

// maxStateBytes bounds what is read back.
const maxStateBytes = 4096

// Persist ties the meter to a session's state file and restores the counters
// found there. It returns whether any were restored. A session with no id, or a
// state file that cannot be read, restores nothing and the meter still works.
func (m *Meter) Persist(sessionID string) bool {
	path, err := config.RunCapsFile(sessionID)
	if err != nil {
		return false
	}
	m.mu.Lock()
	m.stateKey = sessionID
	m.mu.Unlock()
	snap, ok := readState(path)
	if !ok {
		return false
	}
	m.Restore(snap)
	return true
}

// Save writes the counters to the session's state file. It does nothing for a
// meter that was not tied to a session, and never fails the run: the error is
// returned for a caller that wants to say so.
func (m *Meter) Save() error {
	m.mu.Lock()
	key, forgotten := m.stateKey, m.forgotten
	m.mu.Unlock()
	if key == "" || forgotten {
		return nil
	}
	path, err := config.RunCapsFile(key)
	if err != nil {
		return err
	}
	return writeState(path, m.Snapshot())
}

// Forget removes the session's state file, so a later resume starts from zero.
// The turn it was called in saves nothing more.
func (m *Meter) Forget() {
	m.mu.Lock()
	key := m.stateKey
	m.forgotten = true
	m.mu.Unlock()
	if key == "" {
		return
	}
	if path, err := config.RunCapsFile(key); err == nil {
		_ = os.Remove(path)
	}
}

func readState(path string) (Snapshot, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return Snapshot{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, false
	}
	var s Snapshot
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Snapshot{}, false
	}
	if s.CostMicroUSD < 0 || s.Tokens < 0 || s.Passes < 0 || s.Extends < 0 || s.WallMS < 0 {
		return Snapshot{}, false
	}
	return s, true
}

func writeState(path string, s Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("the run-cap state directory is not a plain directory")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("the run-cap state file is not a regular file")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// PruneState removes state files not touched for retention, the session
// retention the ledger uses, and returns how many it removed. A symbolic link
// and anything that is not a state file name is left alone.
func PruneState(retention time.Duration, now time.Time) int {
	dir, err := config.RunCapsDir()
	if err != nil || retention <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= retention {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// String is a one-line description for a log: numbers only.
func (s Snapshot) String() string {
	return fmt.Sprintf("cost=%d tokens=%d passes=%d extends=%d wall_ms=%d", s.CostMicroUSD, s.Tokens, s.Passes, s.Extends, s.WallMS)
}
