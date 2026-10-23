package sessionsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/repomemory"
)

// Memory replication (docs/a2a.md#repo-memory, docs/session-sync.md). The
// shared memory (internal/repomemory: repo notes and fleet profile lessons)
// is uploaded to Vulnetix and pulled back, so it survives an ephemeral
// machine (a Pix Sandbox) and is managed from the Software Factory console.
// It runs only while sync is on. The A2A blackout does not stop it: it is
// persistence, like the session mirror, not relay between agents.

// Memory replication limits.
const (
	memoryUploadBatch = 100
	memoryPullLimit   = 500
	memoryPullPages   = 20
	// MemoryHydrateTimeout bounds the first-start hydration a read waits for.
	MemoryHydrateTimeout = 8 * time.Second
	memoryEvery          = time.Minute
	memoryStateFile      = "replica.json"
	memoryLockFile       = "replica"
)

// MemoryPostResult is what the memory endpoint answers.
type MemoryPostResult struct {
	Accepted int   `json:"accepted"`
	LastSeq  int64 `json:"lastSeq"`
}

// PostMemory uploads local memory changes (new notes and tombstones). The
// server keeps each by noteId, newest change winning.
func (c *Client) PostMemory(ctx context.Context, hostID string, notes []repomemory.Note) (MemoryPostResult, error) {
	var out MemoryPostResult
	if notes == nil {
		notes = []repomemory.Note{}
	}
	err := c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/memory", map[string]any{"notes": notes}, &out, requestTimeout)
	return out, err
}

// MemoryPage is one page of the organisation's memory after a cursor,
// tombstones included.
type MemoryPage struct {
	Notes   []repomemory.Note `json:"notes"`
	LastSeq int64             `json:"lastSeq"`
}

// GetMemory reads the organisation's memory changes after seq; scope and
// scopeKey narrow it when set.
func (c *Client) GetMemory(ctx context.Context, scope, scopeKey string, after int64, limit int) (MemoryPage, error) {
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope)
	}
	if scopeKey != "" {
		q.Set("scopeKey", scopeKey)
	}
	q.Set("after", strconv.FormatInt(after, 10))
	q.Set("limit", strconv.Itoa(limit))
	var out MemoryPage
	err := c.do(ctx, http.MethodGet, "/memory?"+q.Encode(), nil, &out, requestTimeout)
	return out, err
}

// MemoryClient is the part of Client the replicator uses.
type MemoryClient interface {
	PostMemory(ctx context.Context, hostID string, notes []repomemory.Note) (MemoryPostResult, error)
	GetMemory(ctx context.Context, scope, scopeKey string, after int64, limit int) (MemoryPage, error)
}

// memoryState is the host's replication marks: the outbox offset uploaded
// and the server seq pulled.
type memoryState struct {
	Uploaded int64 `json:"uploaded"`
	Pulled   int64 `json:"pulled"`
	Hydrated bool  `json:"hydrated"`
}

// MemoryReplicator uploads the host's memory changes and pulls the
// organisation's. Several processes on a host may run one: the marks are
// shared, and an upload holds the host's lockfile, so a change goes up once
// (and the server would keep it once anyway).
type MemoryReplicator struct {
	client MemoryClient
	hostID string
	root   string
	every  time.Duration
	wake   chan struct{}

	mu      sync.Mutex
	lastErr error
}

// NewMemoryReplicator replicates the memory under root for hostID.
func NewMemoryReplicator(c MemoryClient, hostID, root string) *MemoryReplicator {
	return &MemoryReplicator{client: c, hostID: hostID, root: root, every: memoryEvery, wake: make(chan struct{}, 1)}
}

// Start hydrates a fresh machine in the background, with reads waiting for
// it at most MemoryHydrateTimeout, then replicates until ctx ends.
func (r *MemoryReplicator) Start(ctx context.Context) {
	st := r.state()
	if repomemory.Empty(r.root) && !st.Hydrated {
		done := repomemory.BeginHydrate()
		go func() {
			defer done()
			hctx, cancel := context.WithTimeout(ctx, MemoryHydrateTimeout)
			defer cancel()
			r.setErr(r.Pull(hctx))
		}()
	}
	go r.run(ctx)
}

// Wake asks for a replication pass now (the A2A inbox woke, a note was
// written).
func (r *MemoryReplicator) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// LastError is the last pass's error, or nil.
func (r *MemoryReplicator) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *MemoryReplicator) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *MemoryReplicator) run(ctx context.Context) {
	t := time.NewTicker(r.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
		r.setErr(r.Sync(ctx))
	}
}

// Sync uploads pending changes and pulls new ones.
func (r *MemoryReplicator) Sync(ctx context.Context) error {
	return errors.Join(r.Upload(ctx), r.Pull(ctx))
}

// Upload sends the outbox from the uploaded mark, a batch at a time.
func (r *MemoryReplicator) Upload(ctx context.Context) error {
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return err
	}
	release, err := config.AcquireFileLock(filepath.Join(r.root, memoryLockFile+".lock"))
	if err != nil {
		return err
	}
	defer release()
	for {
		st := r.state()
		notes, next, err := repomemory.Outbox(r.root, st.Uploaded, memoryUploadBatch)
		if err != nil {
			return err
		}
		if next == st.Uploaded && len(notes) == 0 {
			if repomemory.CompactOutbox(r.root, next) {
				st.Uploaded = 0
				r.saveState(st)
			}
			return nil
		}
		for i := range notes {
			if notes[i].HostID == "" {
				notes[i].HostID = r.hostID
			}
			notes[i].Seq = 0
		}
		if len(notes) > 0 {
			if _, err := r.client.PostMemory(ctx, r.hostID, notes); err != nil {
				return fmt.Errorf("memory upload: %w", err)
			}
		}
		st = r.state()
		st.Uploaded = next
		r.saveState(st)
	}
}

// Pull applies the organisation's changes after the pulled mark: a note
// upserted by noteId with the newest change winning, a tombstone removing it.
func (r *MemoryReplicator) Pull(ctx context.Context) error {
	st := r.state()
	after := st.Pulled
	for page := 0; page < memoryPullPages; page++ {
		res, err := r.client.GetMemory(ctx, "", "", after, memoryPullLimit)
		if err != nil {
			return fmt.Errorf("memory pull: %w", err)
		}
		if len(res.Notes) > 0 {
			if _, err := repomemory.Apply(r.root, res.Notes); err != nil {
				return err
			}
		}
		next := after
		for _, n := range res.Notes {
			next = max(next, n.Seq)
		}
		next = max(next, res.LastSeq)
		st = r.state()
		st.Pulled, st.Hydrated = max(st.Pulled, next), true
		r.saveState(st)
		if len(res.Notes) < memoryPullLimit || next == after {
			return nil
		}
		after = next
	}
	return nil
}

func (r *MemoryReplicator) state() memoryState {
	var st memoryState
	data, err := os.ReadFile(filepath.Join(r.root, memoryStateFile))
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (r *MemoryReplicator) saveState(st memoryState) {
	data, _ := json.Marshal(st)
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(r.root, ".replica-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	_ = os.Rename(tmp.Name(), filepath.Join(r.root, memoryStateFile))
}
