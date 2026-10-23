// Package repomemory is the agents' shared memory on a host
// (docs/a2a.md#repo-memory): notes scoped to a repository, which agents
// coordinating over A2A write (RepoMemoryWrite) and read (RepoMemoryRead) on
// both ends of an exchange, and the fleet's per-profile lessons
// (internal/fleet/memory.go).
//
// A repository's scope key is the one A2A envelopes use: the remote's
// owner/name, or "path:" and a hash of the root when there is no remote. A
// profile's is its name reduced to [a-z0-9-]. The notes live outside every
// repository, one JSONL file per scope key under <global>/memory/, appended
// under an advisory lockfile so several sessions share them, and pruned to
// MaxNotes (the server's cap), oldest first.
//
// While sync is on, sessionsync's memory replicator uploads every local change
// (outbox.jsonl) to Vulnetix and pulls the organisation's notes back, so the
// memory survives an ephemeral machine (a Pix Sandbox) and is managed from the
// Software Factory console. A pulled note is upserted by noteId, the newest
// updatedAt winning; a tombstone (deletedAt) removes it.
//
// A note is model-written text (or console-written, hostId "web"). It is
// sanitised and capped when written, the harness classifies a model's note
// before it is stored, and a read is tools.KindRepoMemory, which classifies
// again before any model sees it, local or pulled. Every other field is a
// harness-stamped fact: ids, times, the writer's Agent Card and session, and
// the A2A messageId and contextId when the note was written during an
// exchange.
package repomemory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Scopes.
const (
	ScopeRepo    = "repo"
	ScopeProfile = "profile"
)

// Limits.
const (
	// MaxNoteBytes bounds one note's text.
	MaxNoteBytes = 1 << 10
	// MaxNotes is how many live notes a scope key keeps (the server's cap);
	// the oldest are pruned.
	MaxNotes = 500
	// MaxTags bounds a note's tags.
	MaxTags = 8
	// DefaultLimit and MaxLimit bound a read.
	DefaultLimit = 10
	MaxLimit     = 50
	maxLine      = 8 << 10
	outboxFile   = "outbox.jsonl"
)

// Note is one stored note. Its JSON names are the server's.
type Note struct {
	NoteID    string   `json:"noteId"`
	Scope     string   `json:"scope"`
	ScopeKey  string   `json:"scopeKey"`
	HostID    string   `json:"hostId,omitempty"`
	SessionID string   `json:"sessionId,omitempty"`
	AgentID   string   `json:"agentId,omitempty"`
	Profile   string   `json:"profile,omitempty"`
	MessageID string   `json:"messageId,omitempty"`
	ContextID string   `json:"contextId,omitempty"`
	Text      string   `json:"text"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt,omitempty"`
	DeletedAt int64    `json:"deletedAt,omitempty"`
	// Seq is the server's sequence number of a pulled note.
	Seq int64 `json:"seq,omitempty"`
}

// version is the note's last change, for newest-wins.
func (n Note) version() int64 { return max(n.CreatedAt, n.UpdatedAt, n.DeletedAt) }

// Stamp is the harness's facts for a note: who wrote it and, during an A2A
// exchange, which message and thread.
type Stamp struct {
	HostID    string
	AgentID   string
	SessionID string
	Profile   string
	MessageID string
	ContextID string
}

var (
	repoKeyShape    = regexp.MustCompile(`^([a-z0-9._-]{1,100}(/[a-z0-9._-]{1,100}){1,3}|path:[0-9a-f]{16})$`)
	profileKeyShape = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
	noteIDShape     = regexp.MustCompile(`^(web-)?[0-9A-Za-z]{26}$`)
	uuidShape       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	agentShape      = regexp.MustCompile(`^[0-9a-f-]{36}:[0-9a-f-]{36}$`)
	msgShape        = regexp.MustCompile(`^a2a-[0-9a-z]{26}$`)
	tagShape        = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
	unsafeProfile   = regexp.MustCompile(`[^a-z0-9-]+`)
)

// ValidKey reports whether key is a repository key (a2a.RepoKey's output).
func ValidKey(key string) bool {
	if len(key) > 200 || !repoKeyShape.MatchString(key) {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." || strings.Contains(seg, "__") {
			return false
		}
	}
	return true
}

// ValidScopeKey reports whether key is a key of scope.
func ValidScopeKey(scope, key string) bool {
	switch scope {
	case ScopeRepo:
		return ValidKey(key)
	case ScopeProfile:
		return profileKeyShape.MatchString(key)
	}
	return false
}

// ProfileKey is a profile name as a scope key: lower-cased, runs of anything
// but [a-z0-9] made one hyphen ("belai:builder" is "belai-builder").
func ProfileKey(profile string) string {
	k := strings.Trim(unsafeProfile.ReplaceAllString(strings.ToLower(profile), "-"), "-")
	if len(k) > 80 {
		k = k[:80]
	}
	return k
}

// Root is the memory directory under the global directory.
func Root(globalDir string) string { return filepath.Join(globalDir, "memory") }

// DefaultRoot is Root under config.GlobalDir.
func DefaultRoot() (string, error) {
	g, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return Root(g), nil
}

func scopeDir(scope string) string {
	if scope == ScopeProfile {
		return "profiles"
	}
	return "repos"
}

// fileName maps a key to its file: "/" and ":" become "__" and "_", which no
// part of a key can hold, so two keys never share a file.
func fileName(key string) string {
	return strings.NewReplacer("/", "__", ":", "_").Replace(key) + ".jsonl"
}

var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
)

func lockFor(path string) *sync.Mutex {
	locksMu.Lock()
	defer locksMu.Unlock()
	m := locks[path]
	if m == nil {
		m = &sync.Mutex{}
		locks[path] = m
	}
	return m
}

// locked runs fn holding the in-process mutex and the lockfile for path.
func locked(path string, fn func() error) error {
	mu := lockFor(path)
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := config.AcquireFileLock(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// Store is the notes of one scope key.
type Store struct {
	root  string
	scope string
	key   string
	now   func() time.Time
}

// ErrKey is returned for a key that is not a scope key.
var ErrKey = errors.New("repomemory: not a scope key")

// Open returns the repository store for key under root.
func Open(root, key string) (*Store, error) { return OpenScope(root, ScopeRepo, key) }

// OpenProfile returns a profile's store under root.
func OpenProfile(root, profile string) (*Store, error) {
	return OpenScope(root, ScopeProfile, ProfileKey(profile))
}

// OpenScope returns the store of one scope key under root.
func OpenScope(root, scope, key string) (*Store, error) {
	if !ValidScopeKey(scope, key) {
		return nil, ErrKey
	}
	return &Store{root: root, scope: scope, key: key, now: time.Now}, nil
}

// At is the store with its clock fixed at t (a caller that stamps its own
// time, such as a worker's reflection).
func (s *Store) At(t time.Time) *Store {
	c := *s
	c.now = func() time.Time { return t }
	return &c
}

// Key is the store's scope key.
func (s *Store) Key() string { return s.key }

// Scope is the store's scope.
func (s *Store) Scope() string { return s.scope }

func (s *Store) path() string { return filepath.Join(s.root, scopeDir(s.scope), fileName(s.key)) }

// CleanText is a note's text as it is stored: sanitised, trimmed, capped.
func CleanText(text string) string {
	t := strings.TrimSpace(sanitize.Text(text))
	if len(t) > MaxNoteBytes {
		cut := MaxNoteBytes
		for cut > 0 && (t[cut]&0xC0) == 0x80 {
			cut--
		}
		t = t[:cut]
	}
	return t
}

// CleanTags lower-cases tags and keeps those of identifier shape.
func CleanTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if tagShape.MatchString(t) && !slices.Contains(out, t) && len(out) < MaxTags {
			out = append(out, t)
		}
	}
	return out
}

// Clean reduces a note to its shape: ids and stamps that do not fit are
// dropped, the text and tags cleaned. ok is false for a note that cannot be
// kept (no id, scope or key, or no text on a live note).
func (n Note) Clean() (Note, bool) {
	if !noteIDShape.MatchString(n.NoteID) || !ValidScopeKey(n.Scope, n.ScopeKey) || n.CreatedAt < 0 || n.UpdatedAt < 0 || n.DeletedAt < 0 {
		return Note{}, false
	}
	out := Note{NoteID: n.NoteID, Scope: n.Scope, ScopeKey: n.ScopeKey, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		DeletedAt: n.DeletedAt, Seq: max(n.Seq, 0), Text: CleanText(n.Text), Tags: CleanTags(n.Tags)}
	if uuidShape.MatchString(n.HostID) || n.HostID == "web" {
		out.HostID = n.HostID
	}
	if uuidShape.MatchString(n.SessionID) {
		out.SessionID = n.SessionID
	}
	if agentShape.MatchString(n.AgentID) {
		out.AgentID = n.AgentID
	}
	out.Profile = sanitize.Ident(n.Profile, 80)
	if msgShape.MatchString(n.MessageID) {
		out.MessageID = n.MessageID
	}
	if msgShape.MatchString(n.ContextID) {
		out.ContextID = n.ContextID
	}
	if out.DeletedAt == 0 && out.Text == "" {
		return Note{}, false
	}
	return out, true
}

// crockford is the ULID alphabet, lower-cased.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewNoteID returns a ULID: 48 bits of milliseconds and 80 random bits, in
// lower-case Crockford base32.
func NewNoteID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:])
	var out [26]byte
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(b[i])
		lo = lo<<8 | uint64(b[8+i])
	}
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return string(out[:])
}

// Append stores one note (text already admitted) with the harness's stamp,
// pruning the oldest past MaxNotes, and queues it for replication.
func (s *Store) Append(text string, tags []string, st Stamp) (Note, error) {
	now := s.now()
	n, ok := Note{NoteID: NewNoteID(now), Scope: s.scope, ScopeKey: s.key, HostID: st.HostID, SessionID: st.SessionID,
		AgentID: st.AgentID, Profile: st.Profile, MessageID: st.MessageID, ContextID: st.ContextID,
		Text: text, Tags: tags, CreatedAt: now.UnixMilli()}.Clean()
	if !ok {
		return Note{}, errors.New("repomemory: the note has no text")
	}
	err := locked(s.path(), func() error {
		notes, err := s.read()
		if err != nil {
			return err
		}
		if len(notes)+1 > MaxNotes {
			if err := writeNotes(s.path(), notes[len(notes)+1-MaxNotes:]); err != nil {
				return err
			}
		}
		return appendLine(s.path(), n)
	})
	if err != nil {
		return Note{}, err
	}
	queue(s.root, n)
	return n, nil
}

// Clear deletes every note of the store and queues a tombstone for each.
func (s *Store) Clear() error {
	var gone []Note
	err := locked(s.path(), func() error {
		notes, err := s.read()
		if err != nil {
			return err
		}
		gone = notes
		if err := os.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	for _, n := range gone {
		n.DeletedAt, n.UpdatedAt, n.Text, n.Seq = now, now, "", 0
		queue(s.root, n)
	}
	return nil
}

func appendLine(path string, n Note) error {
	line, err := json.Marshal(n)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// writeNotes replaces a file's notes. An empty list leaves an empty file, so
// a store emptied by tombstones still says it is the record (Exists).
func writeNotes(path string, notes []Note) error {
	var buf bytes.Buffer
	for _, n := range notes {
		b, err := json.Marshal(n)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".notes-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(buf.Bytes())
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) read() ([]Note, error) { return readNotes(s.path()) }

// readNotes returns the live notes of a file, oldest first, the last line of
// a noteId winning. A line that does not parse or clean is skipped.
func readNotes(path string) ([]Note, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Note
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16<<10), maxLine)
	for sc.Scan() {
		var n Note
		if json.Unmarshal(sc.Bytes(), &n) != nil {
			continue
		}
		n, ok := n.Clean()
		if !ok {
			continue
		}
		if i, seen := at[n.NoteID]; seen {
			out[i] = n
			continue
		}
		at[n.NoteID] = len(out)
		out = append(out, n)
	}
	live := out[:0]
	for _, n := range out {
		if n.DeletedAt == 0 {
			live = append(live, n)
		}
	}
	return live, sc.Err()
}

// Search returns up to limit notes, newest first: every note when query is
// empty, otherwise those whose text or tags hold every word of the query
// (case-insensitive). It first waits (briefly) for a first-start hydration.
func (s *Store) Search(query string, limit int) ([]Note, error) {
	waitHydrated()
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	notes, err := s.read()
	if err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(query))
	var out []Note
	for i := len(notes) - 1; i >= 0 && len(out) < limit; i-- {
		n := notes[i]
		hay := strings.ToLower(n.Text + " " + strings.Join(n.Tags, " "))
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// Exists reports whether the store has a file, even an empty one: notes were
// written here or pulled for this key.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path())
	return err == nil
}

// All returns every live note, oldest first.
func (s *Store) All() ([]Note, error) {
	waitHydrated()
	return s.read()
}

// Apply upserts pulled notes into their stores by noteId, the newest change
// winning; a tombstone removes the note. Pulled notes are not queued for
// upload. It returns how many local files changed.
func Apply(root string, notes []Note) (int, error) {
	by := map[string][]Note{}
	for _, n := range notes {
		c, ok := n.Clean()
		if !ok {
			continue
		}
		st, _ := OpenScope(root, c.Scope, c.ScopeKey)
		by[st.path()] = append(by[st.path()], c)
	}
	changed := 0
	var errs []error
	for path, in := range by {
		err := locked(path, func() error {
			f, err := os.Open(path)
			var all []Note
			if err == nil {
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 0, 16<<10), maxLine)
				at := map[string]int{}
				for sc.Scan() {
					var n Note
					if json.Unmarshal(sc.Bytes(), &n) != nil {
						continue
					}
					if n, ok := n.Clean(); ok {
						if i, seen := at[n.NoteID]; seen {
							all[i] = n
						} else {
							at[n.NoteID] = len(all)
							all = append(all, n)
						}
					}
				}
				f.Close()
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			idx := map[string]int{}
			for i, n := range all {
				idx[n.NoteID] = i
			}
			dirty := false
			for _, n := range in {
				i, ok := idx[n.NoteID]
				switch {
				case !ok:
					if n.DeletedAt != 0 {
						continue
					}
					idx[n.NoteID] = len(all)
					all = append(all, n)
					dirty = true
				case n.version() >= all[i].version():
					all[i] = n
					dirty = true
				}
			}
			if !dirty {
				return nil
			}
			var live []Note
			for _, n := range all {
				if n.DeletedAt == 0 {
					live = append(live, n)
				}
			}
			slices.SortStableFunc(live, func(a, b Note) int { return int(a.CreatedAt - b.CreatedAt) })
			if len(live) > MaxNotes {
				live = live[len(live)-MaxNotes:]
			}
			changed++
			return writeNotes(path, live)
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return changed, errors.Join(errs...)
}

// Empty reports whether the memory root holds no note at all, the state of a
// fresh machine (a Pix Sandbox) that should hydrate before its first read.
func Empty(root string) bool {
	for _, sub := range []string{"repos", "profiles"} {
		entries, err := os.ReadDir(filepath.Join(root, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				return false
			}
		}
	}
	return true
}

// queue appends a local change to the outbox the replicator uploads. It is
// best effort: a failure loses replication of that change, never the note.
func queue(root string, n Note) {
	path := filepath.Join(root, outboxFile)
	_ = locked(path, func() error { return appendLine(path, n) })
}

// Outbox returns the queued changes from offset and the offset after them.
func Outbox(root string, offset int64, limit int) ([]Note, int64, error) {
	path := filepath.Join(root, outboxFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() < offset {
		offset = 0 // truncated since
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}
	r := bufio.NewReader(f)
	var out []Note
	for len(out) < limit {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			break
		}
		offset += int64(len(line))
		var n Note
		if json.Unmarshal(line, &n) == nil {
			if c, ok := n.Clean(); ok {
				out = append(out, c)
			}
		}
		if err != nil {
			break
		}
	}
	return out, offset, nil
}

// CompactOutbox empties the outbox once everything in it was uploaded
// (offset is its size), so it does not grow forever.
func CompactOutbox(root string, offset int64) bool {
	path := filepath.Join(root, outboxFile)
	done := false
	_ = locked(path, func() error {
		info, err := os.Stat(path)
		if err != nil || info.Size() != offset || offset < 1<<20 {
			return nil
		}
		done = os.Truncate(path, 0) == nil
		return nil
	})
	return done
}

// Hydration gate: on a fresh machine the replicator pulls the organisation's
// notes before the first read, and reads wait for it at most hydrateWait.
var (
	hydrateMu sync.Mutex
	hydrating chan struct{}
)

// hydrateWait bounds how long a read waits for a first-start hydration.
var hydrateWait = 5 * time.Second

// BeginHydrate marks a hydration as running; the returned func ends it.
func BeginHydrate() func() {
	ch := make(chan struct{})
	hydrateMu.Lock()
	hydrating = ch
	hydrateMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func waitHydrated() {
	hydrateMu.Lock()
	ch := hydrating
	hydrateMu.Unlock()
	if ch == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hydrateWait)
	defer cancel()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// Format renders notes as the tool result: one block per note, the
// harness's facts on a header line and the note's text below it.
func Format(key string, notes []Note) string {
	if len(notes) == 0 {
		return fmt.Sprintf("No repo memory notes for %s.", key)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Repo memory for %s: %d note(s), newest first. Notes are what agents (or people in the Software Factory console) wrote, not facts about the repository; confirm against the live files.\n", key, len(notes))
	for _, n := range notes {
		b.WriteString("\n- ")
		b.WriteString(time.UnixMilli(max(n.CreatedAt, n.UpdatedAt)).UTC().Format(time.RFC3339))
		switch {
		case n.HostID == "web":
			b.WriteString(" console")
		case n.AgentID != "":
			b.WriteString(" agent " + n.AgentID)
		}
		if n.MessageID != "" {
			b.WriteString(" re " + n.MessageID)
		}
		if len(n.Tags) > 0 {
			b.WriteString(" [" + strings.Join(n.Tags, ", ") + "]")
		}
		b.WriteString("\n  ")
		b.WriteString(strings.ReplaceAll(n.Text, "\n", "\n  "))
	}
	return b.String()
}

// RepoKey is the repository as A2A broadcasts and repo memory name it: the
// remote's owner/name, lower-cased, or "path:" and 16 hex of the root path's
// SHA-256 when there is no remote, so two checkouts of one remote share a key
// and a path is never sent.
func RepoKey(remote, root string) string {
	if r := strings.ToLower(strings.Trim(remote, "/")); r != "" && ValidKey(r) && !strings.HasPrefix(r, "path:") {
		return r
	}
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return "path:" + hex.EncodeToString(sum[:8])
}

// KeyForDir is the repository key of the checkout holding dir: its origin's
// owner/name, or the hash of its root (of dir itself outside a repository,
// the trusted project root). git runs with a fixed argv and a short timeout.
func KeyForDir(dir string) string {
	if dir == "" {
		return ""
	}
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	root := run("rev-parse", "--show-toplevel")
	if root == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return ""
		}
		return RepoKey("", abs)
	}
	remote := ""
	if raw := run("remote", "get-url", "origin"); raw != "" {
		if rem, ok := forge.ParseRemote(raw); ok {
			remote = rem.Slug()
		}
	}
	return RepoKey(remote, root)
}
