package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// maxListBytes is the largest session file SessionsIn will parse for its
// listing fields. Above this the file is still listed (with a fallback
// display name) but not read, so a pathological session cannot stall /resume.
const maxListBytes = 32 << 20

// Prune removes session files older than maxAge, best-effort per file.
// skipPaths lists exact session files to protect regardless of age (e.g. the
// session just resumed via --resume).
func (s *Store) Prune(maxAge time.Duration, skipPaths ...string) (removed int, err error) {
	if _, err := os.Stat(s.Root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	skip := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		if p != "" {
			skip[p] = true
		}
	}
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.Walk(s.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		if skip[path] {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	return removed, nil
}

// Store persists JSONL session trees under a root directory. Each working
// directory maps to its own sub-directory, and each session is a single
// append-only .jsonl file of Entry records.
type Store struct {
	// Root is the absolute path holding one sub-directory per project key.
	Root string
}

// NewStore returns a Store rooted at <GlobalDir>/sessions.
func NewStore() (*Store, error) {
	dir, err := config.SessionsDir()
	if err != nil {
		return nil, fmt.Errorf("locate global dir: %w", err)
	}
	return &Store{Root: dir}, nil
}

// NewStoreAt returns a Store rooted at an explicit path (used by tests).
func NewStoreAt(root string) *Store {
	return &Store{Root: root}
}

// WorkdirKey derives a filesystem-safe, deterministic directory name from an
// absolute working-directory path: "<basename>-<8 hex chars of sha256>".
// The body lives in config.WorkdirKey so config-backed per-project stores can
// reuse the exact same key without importing session (which imports config).
func WorkdirKey(abs string) string { return config.WorkdirKey(abs) }

// SessionInfo describes a stored session for listing/resume.
type SessionInfo struct {
	ID          string
	DisplayName string
	Path        string
	ModTime     int64

	// Key and Workdir address the project the session belongs to. Workdir is
	// "" when the recorded cwd cannot be recovered or verified (legacy files).
	Key     Key
	Workdir string

	// Turns counts the user prompts in the session; the listing uses it as a
	// cheap size signal without parsing provider-shaped transcript.
	Turns     int
	Model     string
	Provider  string
	Compacted bool // the session begins from a compaction summary
	HasTools  bool // the session recorded at least one tool entry
}

// dirForKey returns the per-key directory under Root.
func (s *Store) dirForKey(k Key) string { return filepath.Join(s.Root, string(k)) }

// keyFor derives a Key from a workdir.
func keyFor(workdir string) (Key, error) { return KeyFor(workdir) }

// sessionPathForKey returns the .jsonl path for a fully-resolved session id.
func (s *Store) sessionPathForKey(k Key, sessionID string) string {
	return filepath.Join(s.dirForKey(k), sessionID+".jsonl")
}

// SessionPath returns the on-disk .jsonl path for a session under a project
// key. It is the exported form of sessionPathForKey, used by the exit card to
// print the durable location of a finished session.
func (s *Store) SessionPath(k Key, sessionID string) string {
	return s.sessionPathForKey(k, sessionID)
}

// sessionPath is the workdir-addressed form, kept for the public wrappers.
func (s *Store) sessionPath(workdir, sessionID string) (string, error) {
	k, err := keyFor(workdir)
	if err != nil {
		return "", err
	}
	return s.sessionPathForKey(k, sessionID), nil
}

// lookupIn returns candidate session ids for an exact-or-prefix match under
// one key. It does not error when nothing matches.
func (s *Store) lookupIn(k Key, idOrPrefix string) ([]string, error) {
	dir := s.dirForKey(k)
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

// resolveIn maps a full or partial session id to a full id under one key.
func (s *Store) resolveIn(k Key, idOrPrefix string) (string, error) {
	if idOrPrefix == "" {
		return "", errors.New("session id is empty")
	}
	matches, err := s.lookupIn(k, idOrPrefix)
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

// resolveForAppendIn is like resolveIn but treats a non-matching id as a
// brand-new session id (append-only stores create on demand).
func (s *Store) resolveForAppendIn(k Key, idOrPrefix string) (string, error) {
	if idOrPrefix == "" {
		return "", errors.New("session id is empty")
	}
	matches, err := s.lookupIn(k, idOrPrefix)
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

// Resolve maps a full or partial session id to a full id for a workdir.
func (s *Store) Resolve(workdir, idOrPrefix string) (string, error) {
	k, err := keyFor(workdir)
	if err != nil {
		return "", err
	}
	return s.ResolveIn(k, idOrPrefix)
}

// ResolveIn maps a full or partial session id to a full id for a key.
func (s *Store) ResolveIn(k Key, idOrPrefix string) (string, error) {
	return s.resolveIn(k, idOrPrefix)
}

// resolveForAppend is like Resolve but treats a non-matching id as a brand-new
// session id (append-only stores create on demand).
func (s *Store) resolveForAppend(workdir, idOrPrefix string) (string, error) {
	k, err := keyFor(workdir)
	if err != nil {
		return "", err
	}
	return s.resolveForAppendIn(k, idOrPrefix)
}

// Append appends one entry to a session, creating the session file on first
// use. The entry ID and timestamp are filled in when absent.
func (s *Store) Append(workdir, sessionID string, e Entry) error {
	k, err := keyFor(workdir)
	if err != nil {
		return err
	}
	return s.AppendTo(k, sessionID, e)
}

// AppendTo is the key-addressed Append.
func (s *Store) AppendTo(k Key, sessionID string, e Entry) error {
	id, err := s.resolveForAppendIn(k, sessionID)
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
	path := s.sessionPathForKey(k, id)
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

// readEntriesFile reads and parses one session file into append-ordered
// entries. It is the single scanner for Read/ReadFrom/SessionsIn so the three
// can never drift on buffer limits or blank-line handling.
func readEntriesFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session file %s not found", path)
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

// Read returns all entries of a session in append order, for a workdir.
func (s *Store) Read(workdir, sessionID string) ([]Entry, error) {
	k, err := keyFor(workdir)
	if err != nil {
		return nil, err
	}
	return s.ReadFrom(k, sessionID)
}

// ReadFrom is the key-addressed Read.
func (s *Store) ReadFrom(k Key, sessionID string) ([]Entry, error) {
	id, err := s.resolveIn(k, sessionID)
	if err != nil {
		return nil, err
	}
	path := s.sessionPathForKey(k, id)
	entries, err := readEntriesFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
		return nil, err
	}
	return entries, nil
}

// Fork copies a parent session's history into a new session id so the two can
// diverge. The new session must not already exist.
func (s *Store) Fork(workdir, parentSessionID, newSessionID string) error {
	k, err := keyFor(workdir)
	if err != nil {
		return err
	}
	return s.ForkAcross(k, parentSessionID, k, newSessionID)
}

// ForkAcross copies a parent session's history from src into a new session id
// under dst. The destination must not already exist (O_EXCL, matching Fork).
func (s *Store) ForkAcross(src Key, srcID string, dst Key, dstID string) error {
	entries, err := s.ReadFrom(src, srcID)
	if err != nil {
		return err
	}
	path := s.sessionPathForKey(dst, dstID)
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
	k, err := keyFor(workdir)
	if err != nil {
		return nil, err
	}
	return s.SessionsIn(k)
}

// SessionsIn lists stored sessions for a key, most recently modified first.
// It parses each file once via readEntriesFile (the path it already holds),
// avoiding the O(n²) Read-per-file re-resolution. Oversized files are listed
// without parsing.
func (s *Store) SessionsIn(k Key) ([]SessionInfo, error) {
	dir := s.dirForKey(k)
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
		var entries []Entry
		if fi.Size() <= maxListBytes {
			entries, err = readEntriesFile(path)
			if err != nil {
				return nil, err
			}
		}
		infos = append(infos, sessionInfoFromEntries(entries, id, path, fi.ModTime().UnixMilli(), k))
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ModTime != infos[j].ModTime {
			return infos[i].ModTime > infos[j].ModTime
		}
		return infos[i].ID < infos[j].ID
	})
	return infos, nil
}

// sessionInfoFromEntries builds a SessionInfo from parsed entries (nil for an
// oversized file, which degrades to a fallback display name).
func sessionInfoFromEntries(entries []Entry, id, path string, modTime int64, k Key) SessionInfo {
	info := SessionInfo{ID: id, Path: path, ModTime: modTime, Key: k}
	if len(entries) == 0 {
		info.DisplayName = shortSessionID(id)
		return info
	}
	info.DisplayName = DisplayName(entries, id)
	for _, e := range entries {
		if e.Type == "user" || e.Role == "user" {
			info.Turns++
		}
		if e.Type == "summary" {
			info.Compacted = true
		}
		if e.Type == "tool" {
			info.HasTools = true
		}
		if e.Type == "assistant" && e.Meta != nil {
			if v, ok := e.Meta["model"].(string); ok && info.Model == "" {
				info.Model = v
			}
			if v, ok := e.Meta["provider"].(string); ok && info.Provider == "" {
				info.Provider = v
			}
		}
	}
	return info
}

func shortSessionID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// Keys lists every project-key directory under Root.
func (s *Store) Keys() ([]Key, error) {
	des, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var keys []Key
	for _, de := range des {
		if de.IsDir() {
			keys = append(keys, Key(de.Name()))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, nil
}

// UserPrompts returns all unique user-typed prompts across every stored
// session for a workdir, most recently appended first. Duplicates are
// deduplicated while preserving the first (most recent) occurrence.
func (s *Store) UserPrompts(workdir string) ([]string, error) {
	all, err := s.TimedUserPrompts(workdir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for i := len(all) - 1; i >= 0; i-- {
		if p := all[i].Content; !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// TimedPrompt is one user-typed prompt and when it was written, in Unix
// milliseconds.
type TimedPrompt struct {
	Content   string
	Timestamp int64
}

// TimedUserPrompts returns every user-typed prompt across the stored sessions
// for a workdir, oldest first, duplicates included. An entry written without a
// timestamp takes its session file's modification time, so it still orders
// against history kept outside the session store.
func (s *Store) TimedUserPrompts(workdir string) ([]TimedPrompt, error) {
	infos, err := s.Sessions(workdir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Oldest session first, so entries appended in order read oldest first
	// across the whole store.
	sort.Slice(infos, func(i, j int) bool { return infos[i].ModTime < infos[j].ModTime })
	var all []TimedPrompt
	for _, info := range infos {
		entries, err := s.Read(workdir, info.ID)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if (e.Type == "user" || e.Role == "user") && e.Content != "" {
				ts := e.Timestamp
				if ts == 0 {
					ts = info.ModTime
				}
				all = append(all, TimedPrompt{Content: e.Content, Timestamp: ts})
			}
		}
	}
	return all, nil
}
