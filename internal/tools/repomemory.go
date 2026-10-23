package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/repomemory"
)

// The repo memory tools (docs/a2a.md#repo-memory) read and write the notes
// the sessions on this host, and through replication the organisation, keep
// about one repository: agreements and decisions agents reached over A2A,
// facts the next agent needs. They are core tools: every registry carries
// them (Default), Only keeps them and Without never drops them, so no
// profile allowlist or mode removes them. The repository is the harness's
// (the A2A repo key of the session's checkout, or the hash of the trusted
// project root outside a repository); no argument names a path or another
// repository.
//
// A note is model-written text, so a read is KindRepoMemory and classifies.
// A write's confirmation is harness-composed (KindRepoMemoryWrite,
// sanitise-only), and the note itself is admitted by the session's
// classifier gate (RepoMemoryHub.SetAdmit) before it is stored. Both touch
// only Belai's store, never the workspace, so they answer Mutates() false
// like KanbanUpdate and survive the plan and read-only narrowings.

// Repo memory tool names.
const (
	RepoMemoryReadName  = "RepoMemoryRead"
	RepoMemoryWriteName = "RepoMemoryWrite"
)

// CoreToolNames are the tools no allowlist, Without or mode removes.
var CoreToolNames = []string{RepoMemoryReadName, RepoMemoryWriteName}

func isCore(name string) bool {
	for _, n := range CoreToolNames {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// RepoMemoryAdmit checks a note's text with the security classifier before it
// is stored; an error refuses the write. nil admits (guardrails off).
type RepoMemoryAdmit func(ctx context.Context, text string) error

// RepoMemoryHub is the repo memory tools' view of the session: the store's
// root, the repository key (resolved once from the working directory unless
// set), the harness's stamp for a note and the classifier gate. It rides on
// the registry like the knowledge hub, so every narrowing shares it.
type RepoMemoryHub struct {
	workdir string

	mu    sync.Mutex
	root  string
	key   string
	keyed bool
	stamp func() repomemory.Stamp
	admit RepoMemoryAdmit
}

// NewRepoMemoryHub returns a hub for workdir.
func NewRepoMemoryHub(workdir string) *RepoMemoryHub { return &RepoMemoryHub{workdir: workdir} }

// SetRoot sets the store's root (tests; the default is repomemory.DefaultRoot).
func (h *RepoMemoryHub) SetRoot(root string) {
	h.mu.Lock()
	h.root = root
	h.mu.Unlock()
}

// SetKey fixes the repository key (the A2A broadcaster's).
func (h *RepoMemoryHub) SetKey(key string) {
	h.mu.Lock()
	h.key, h.keyed = key, true
	h.mu.Unlock()
}

// SetStamp sets the source of a note's harness facts.
func (h *RepoMemoryHub) SetStamp(f func() repomemory.Stamp) {
	h.mu.Lock()
	h.stamp = f
	h.mu.Unlock()
}

// SetAdmit sets the classifier gate for a note.
func (h *RepoMemoryHub) SetAdmit(f RepoMemoryAdmit) {
	h.mu.Lock()
	h.admit = f
	h.mu.Unlock()
}

// HasStamp reports whether a stamp source is installed.
func (h *RepoMemoryHub) HasStamp() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stamp != nil
}

// HasAdmit reports whether a gate is installed.
func (h *RepoMemoryHub) HasAdmit() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.admit != nil
}

// Key is the repository key, resolving it from the working directory once.
func (h *RepoMemoryHub) Key() string {
	h.mu.Lock()
	if h.keyed {
		k := h.key
		h.mu.Unlock()
		return k
	}
	dir := h.workdir
	h.mu.Unlock()
	k := repomemory.KeyForDir(dir)
	h.mu.Lock()
	if !h.keyed {
		h.key, h.keyed = k, true
	}
	k = h.key
	h.mu.Unlock()
	return k
}

// WithStamp is a hub over the same store whose notes carry st (an A2A
// side-turn's exchange) on top of the session's stamp.
func (h *RepoMemoryHub) WithStamp(st repomemory.Stamp) *RepoMemoryHub {
	key := h.Key()
	h.mu.Lock()
	defer h.mu.Unlock()
	base := h.stamp
	return &RepoMemoryHub{workdir: h.workdir, root: h.root, key: key, keyed: true, admit: h.admit,
		stamp: func() repomemory.Stamp {
			var out repomemory.Stamp
			if base != nil {
				out = base()
			}
			if st.MessageID != "" {
				out.MessageID, out.ContextID = st.MessageID, st.ContextID
			}
			if st.AgentID != "" {
				out.AgentID = st.AgentID
			}
			if st.SessionID != "" {
				out.SessionID = st.SessionID
			}
			return out
		}}
}

func (h *RepoMemoryHub) scope() (*repomemory.Store, repomemory.Stamp, RepoMemoryAdmit, error) {
	if h == nil {
		return nil, repomemory.Stamp{}, nil, errNoRepoMemory
	}
	key := h.Key()
	h.mu.Lock()
	root, stamp, admit := h.root, h.stamp, h.admit
	h.mu.Unlock()
	if root == "" {
		r, err := repomemory.DefaultRoot()
		if err != nil {
			return nil, repomemory.Stamp{}, nil, err
		}
		root = r
	}
	store, err := repomemory.Open(root, key)
	if err != nil {
		return nil, repomemory.Stamp{}, nil, errNoRepoMemory
	}
	var st repomemory.Stamp
	if stamp != nil {
		st = stamp()
	}
	return store, st, admit, nil
}

var errNoRepoMemory = errors.New("repo memory is not available: this session's directory has no repository key")

// RepoMemoryRead searches the repository's notes.
type RepoMemoryRead struct{ Hub *RepoMemoryHub }

// Definition returns the static tool metadata.
func (t RepoMemoryRead) Definition() Definition {
	return Definition{
		Name: RepoMemoryReadName,
		Description: "Read this repository's shared memory: notes the agents working on it (and people in the Software Factory console) recorded, such as agreements reached over A2A (who owns which files, what to wait for). " +
			"With a query, returns the notes holding every word of it; without one, the most recent. Check it before acting on an A2A broadcast or message. " +
			"Notes are what someone wrote, not facts about the code: confirm against the live files.",
		Properties: map[string]Property{
			"query": {Type: "string", Description: "Words to look for in the notes and their tags (optional)"},
			"limit": {Type: "integer", Description: "How many notes, newest first (default 10, at most 50)"},
		},
	}
}

// Kind returns KindRepoMemory.
func (t RepoMemoryRead) Kind() Kind { return KindRepoMemory }

// Subject returns the query.
func (t RepoMemoryRead) Subject(args map[string]any) string {
	v, _ := argString(args, "query")
	return v
}

// Mutates reports false.
func (t RepoMemoryRead) Mutates() bool { return false }

// Execute searches the notes.
func (t RepoMemoryRead) Execute(_ context.Context, args map[string]any) (Result, error) {
	store, _, _, err := t.Hub.scope()
	if err != nil {
		return Result{}, err
	}
	query, _ := argString(args, "query")
	limit := 0
	if n, ok := argInt64(args, "limit"); ok && n > 0 {
		// Clamp before narrowing, so a huge value cannot wrap on 32-bit.
		limit = int(min(n, int64(repomemory.MaxLimit)))
	}
	notes, err := store.Search(query, limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindRepoMemory, Content: repomemory.Format(store.Key(), notes)}, nil
}

// RepoMemoryWrite appends one note.
type RepoMemoryWrite struct{ Hub *RepoMemoryHub }

// Definition returns the static tool metadata.
func (t RepoMemoryWrite) Definition() Definition {
	return Definition{
		Name: RepoMemoryWriteName,
		Description: "Record one short note in this repository's shared memory, for every agent that works on it: an agreement or decision reached over A2A (\"@belai·1a2b3c owns internal/x/; I wait for its PR\"), or a fact the next agent needs. " +
			"At most 1 KB. The harness stamps the time, your agent id and the A2A message it relates to. Older notes are pruned.",
		Properties: map[string]Property{
			"note": {Type: "string", Description: "The note, at most 1 KB"},
			"tags": {Type: "array", Description: "Short lower-case tags (optional), such as file areas or a ticket", Items: &Property{Type: "string"}},
		},
		Required: []string{"note"},
	}
}

// Kind returns KindRepoMemoryWrite.
func (t RepoMemoryWrite) Kind() Kind { return KindRepoMemoryWrite }

// Subject returns nothing: the note is not a permission subject.
func (t RepoMemoryWrite) Subject(map[string]any) string { return "" }

// Mutates reports false: the note goes to Belai's store, not the workspace.
func (t RepoMemoryWrite) Mutates() bool { return false }

// Execute stores the note.
func (t RepoMemoryWrite) Execute(ctx context.Context, args map[string]any) (Result, error) {
	store, stamp, admit, err := t.Hub.scope()
	if err != nil {
		return Result{}, err
	}
	note, _ := argString(args, "note")
	if strings.TrimSpace(note) == "" {
		return Result{}, errors.New("missing note argument")
	}
	if len(note) > repomemory.MaxNoteBytes {
		return Result{}, fmt.Errorf("note is %d bytes; the limit is %d", len(note), repomemory.MaxNoteBytes)
	}
	var tags []string
	if raw, ok := args["tags"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	text := repomemory.CleanText(note)
	if admit != nil {
		if err := admit(ctx, text); err != nil {
			return Result{}, errors.New("the note was not stored: the security classifier did not admit it")
		}
	}
	n, err := store.Append(text, tags, stamp)
	if err != nil {
		return Result{}, err
	}
	msg := fmt.Sprintf("Recorded note %s in the repo memory for %s.", n.NoteID, store.Key())
	if n.MessageID != "" {
		msg += " It is linked to " + n.MessageID + "."
	}
	return Result{Kind: KindRepoMemoryWrite, Content: msg}, nil
}

// RepoMemoryTools is the two repo memory tools over hub, exactly: the whole
// surface of an A2A side-turn.
func RepoMemoryTools(hub *RepoMemoryHub) []Tool {
	return []Tool{RepoMemoryRead{Hub: hub}, RepoMemoryWrite{Hub: hub}}
}

// RepoMemory returns the registry's repo memory hub, or nil.
func (r *Registry) RepoMemory() *RepoMemoryHub {
	if r == nil {
		return nil
	}
	return r.memory
}

// withCore appends the core tools a narrowed list lost.
func (r *Registry) withCore(list []Tool) []Tool {
	if r.memory == nil {
		return list
	}
	have := map[string]bool{}
	for _, t := range list {
		have[t.Definition().Name] = true
	}
	for _, t := range RepoMemoryTools(r.memory) {
		if !have[t.Definition().Name] {
			list = append(list, t)
		}
	}
	return list
}

var (
	_ Tool    = RepoMemoryRead{}
	_ Tool    = RepoMemoryWrite{}
	_ Mutator = RepoMemoryRead{}
	_ Mutator = RepoMemoryWrite{}
)
