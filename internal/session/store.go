package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store persists JSONL session trees under a root directory. Each working
// directory maps to its own sub-directory, and each session is a single
// append-only .jsonl file of Entry records.
type Store struct {
	// Root is the absolute path holding one sub-directory per workdir.
	Root string
}

// NewStore returns a Store rooted at ~/.signet/sessions.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home dir: %w", err)
	}
	return &Store{Root: filepath.Join(home, ".signet", "sessions")}, nil
}

// NewStoreAt returns a Store rooted at an explicit path (used by tests).
func NewStoreAt(root string) *Store {
	return &Store{Root: root}
}

// WorkdirKey derives a filesystem-safe, deterministic directory name from an
// absolute working-directory path: "<basename>-<8 hex chars of sha256>".
func WorkdirKey(abs string) string {
	clean := filepath.Clean(abs)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "root"
	}
	sum := sha256.Sum256([]byte(clean))
	return base + "-" + hex.EncodeToString(sum[:4])
}

// SessionInfo describes a stored session for listing/resume.
type SessionInfo struct {
	ID          string
	DisplayName string
	Path        string
	ModTime     int64
}

// dirFor returns the per-workdir directory under Root.
func (s *Store) dirFor(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	return filepath.Join(s.Root, WorkdirKey(abs)), nil
}

// sessionPath returns the .jsonl path for a fully-resolved session id.
func (s *Store) sessionPath(workdir, sessionID string) (string, error) {
	dir, err := s.dirFor(workdir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

// lookup returns candidate session ids for an exact-or-prefix match. It does
// not error when nothing matches.
func (s *Store) lookup(workdir, idOrPrefix string) ([]string, error) {
	dir, err := s.dirFor(workdir)
	if err != nil {
		return nil, err
	}
	exact := filepath.Join(dir, idOrPrefix+".jsonl")
	if _, err := os.Stat(exact); err == nil {
		return []string{idOrPrefix}, nil
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var matches []string
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		name := strings.TrimSuffix(de.Name(), ".jsonl")
		if strings.HasPrefix(name, idOrPrefix) {
			matches = append(matches, name)
		}
	}
	return matches, nil
}

// Resolve maps a full or partial session id to a full id. A partial id must
// match exactly one stored session.
func (s *Store) Resolve(workdir, idOrPrefix string) (string, error) {
	if idOrPrefix == "" {
		return "", errors.New("session id is empty")
	}
	matches, err := s.lookup(workdir, idOrPrefix)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matching %q", idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous session prefix %q matches %d sessions", idOrPrefix, len(matches))
	}
}

// resolveForAppend is like Resolve but treats a non-matching id as a brand-new
// session id (append-only stores create on demand).
func (s *Store) resolveForAppend(workdir, idOrPrefix string) (string, error) {
	if idOrPrefix == "" {
		return "", errors.New("session id is empty")
	}
	matches, err := s.lookup(workdir, idOrPrefix)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return idOrPrefix, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous session prefix %q matches %d sessions", idOrPrefix, len(matches))
	}
}

// Append appends one entry to a session, creating the session file on first
// use. The entry ID and timestamp are filled in when absent.
func (s *Store) Append(workdir, sessionID string, e Entry) error {
	id, err := s.resolveForAppend(workdir, sessionID)
	if err != nil {
		return err
	}
	if e.ID == "" {
		e.ID, err = NewID()
		if err != nil {
			return err
		}
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	path, err := s.sessionPath(workdir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	defer f.Close()
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append entry: %w", err)
	}
	return nil
}

// Read returns all entries of a session in append order.
func (s *Store) Read(workdir, sessionID string) ([]Entry, error) {
	id, err := s.Resolve(workdir, sessionID)
	if err != nil {
		return nil, err
	}
	path, err := s.sessionPath(workdir, id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse entry in %s: %w", path, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Fork copies a parent session's history into a new session id so the two can
// diverge. The new session must not already exist.
func (s *Store) Fork(workdir, parentSessionID, newSessionID string) error {
	entries, err := s.Read(workdir, parentSessionID)
	if err != nil {
		return err
	}
	path, err := s.sessionPath(workdir, newSessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create forked session: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("write forked entry: %w", err)
		}
	}
	return w.Flush()
}

// Sessions lists stored sessions for a workdir, most recently modified first.
func (s *Store) Sessions(workdir string) ([]SessionInfo, error) {
	dir, err := s.dirFor(workdir)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var infos []SessionInfo
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(de.Name(), ".jsonl")
		path := filepath.Join(dir, de.Name())
		fi, err := de.Info()
		if err != nil {
			return nil, err
		}
		entries, err := s.Read(workdir, id)
		if err != nil {
			return nil, err
		}
		infos = append(infos, SessionInfo{
			ID:          id,
			DisplayName: DisplayName(entries, id),
			Path:        path,
			ModTime:     fi.ModTime().UnixMilli(),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ModTime != infos[j].ModTime {
			return infos[i].ModTime > infos[j].ModTime
		}
		return infos[i].ID < infos[j].ID
	})
	return infos, nil
}
