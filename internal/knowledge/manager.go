package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// Profile names the documents an agent profile lists. ID keys the index on
// disk, Name labels its addresses, and Paths are the user's own files and
// directories (the profile file is the user's, never a model's or a
// repository's).
type Profile struct {
	ID    string
	Name  string
	Paths []string
}

// Options says what a Store covers and how big each part may be.
type Options struct {
	// Root is the trusted repository root whose .vulnetix output is indexed;
	// empty means no project index.
	Root string
	// Profile is the agent profile whose documents are indexed, or nil.
	Profile *Profile
	// IndexTokens caps the profile's corpus, ProjectTokens the project's
	// (.vulnetix output and session files together); zero or less means the
	// config default.
	IndexTokens, ProjectTokens int
	// ProfileGate and ProjectGate admit chunks at ingestion; nil is sanitising
	// alone (the guardrails-off posture).
	ProfileGate, ProjectGate Gate
}

// Report is what a Refresh did, as counts and harness-worded warnings only.
type Report struct {
	Profile, Project Stats
	Warnings         []string
}

// Store owns the indexes a session searches: the profile's, the project's and
// the session's own. Searches see whatever is loaded; Refresh brings the
// persistent two in line with their sources in the background. The profile can
// change under a live session (SetProfile), which is how the TUI follows the
// agent the user engages.
type Store struct {
	opts    Options
	projDir string

	refresh sync.Mutex // one Refresh at a time

	project *Index
	session *Index

	mu          sync.Mutex // guards what follows
	prof        *Profile
	profile     *Index
	profDir     string
	warnings    []string
	running     bool
	again       bool
	lastRefresh time.Time
}

// Open loads the persisted indexes. It never fails the session: an index file
// that is missing is empty, one that fails its integrity check is replaced by
// an in-memory empty one (and never overwritten on disk), and each such case
// is a warning.
func Open(o Options) *Store {
	if o.IndexTokens <= 0 {
		o.IndexTokens = config.DefaultKnowledgeIndexTokens
	}
	if o.ProjectTokens <= 0 {
		o.ProjectTokens = config.DefaultKnowledgeProjectTokens
	}
	s := &Store{opts: o, session: NewIndex(SessionScope)}
	s.SetProfile(o.Profile)
	if o.Root != "" {
		dir, err := config.ProjectKnowledgeDir(o.Root)
		if err != nil {
			s.warn("knowledge: project index unavailable: " + err.Error())
		} else {
			s.projDir = dir
			s.project = s.load(dir, ProjectScope, "project")
			s.project.Fit(o.ProjectTokens)
		}
	}
	return s
}

// SetProfile makes p the profile whose documents are searchable, or with nil
// or no paths none. The same profile (id, name and paths) is a no-op; a
// different one loads its persisted index at once and waits for Refresh to
// bring it up to date.
func (s *Store) SetProfile(p *Profile) {
	if s == nil {
		return
	}
	if p != nil && len(p.Paths) == 0 {
		p = nil
	}
	s.mu.Lock()
	if sameProfile(s.prof, p) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	var (
		ix  *Index
		dir string
	)
	if p != nil {
		var err error
		if dir, err = config.ProfileKnowledgeDir(p.ID); err != nil {
			s.warn("knowledge: profile index unavailable: " + err.Error())
			p = nil
		} else {
			ix = s.load(dir, p.Name, "profile")
			idx, _ := s.limits()
			ix.Fit(idx)
		}
	}
	s.mu.Lock()
	s.prof, s.profile, s.profDir = p, ix, dir
	s.mu.Unlock()
}

func sameProfile(a, b *Profile) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.Name == b.Name && slices.Equal(a.Paths, b.Paths)
}

func (s *Store) load(dir, name, what string) *Index {
	ix, err := Load(dir, name)
	if err != nil {
		s.warn(fmt.Sprintf("knowledge: the %s index failed to load and is empty for this session (%v)", what, errors.Unwrap(err)))
		return NewIndex(name)
	}
	return ix
}

func (s *Store) warn(w string) {
	s.mu.Lock()
	s.warnings = append(s.warnings, w)
	s.mu.Unlock()
}

// Warnings returns what Open and Refresh reported.
func (s *Store) Warnings() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.warnings...)
}

// Set returns the merged view a session searches: profile, project, session.
// It follows the store: a profile set later is searched by a Set taken earlier.
func (s *Store) Set() *Set {
	if s == nil {
		return nil
	}
	return &Set{fn: s.indexes}
}

func (s *Store) indexes() []*Index {
	s.mu.Lock()
	prof := s.profile
	s.mu.Unlock()
	var out []*Index
	for _, ix := range []*Index{prof, s.project, s.session} {
		if ix != nil {
			out = append(out, ix)
		}
	}
	return out
}

// Refresh re-reads the profile's documents and the project's .vulnetix output,
// ingests what changed through the gates, drops what has gone, and saves the
// indexes. It is safe to run while searches go on. Only a cancelled context
// returns an error; every other problem is a warning in the report.
func (s *Store) Refresh(ctx context.Context) (Report, error) {
	var rep Report
	if s == nil {
		return rep, nil
	}
	s.refresh.Lock()
	defer s.refresh.Unlock()
	s.mu.Lock()
	prof, pix, pdir := s.prof, s.profile, s.profDir
	opts := s.opts
	s.mu.Unlock()
	if pix != nil && prof != nil {
		st, err := SyncProfile(ctx, pix, prof.Name, prof.Paths, opts.ProfileGate, opts.IndexTokens)
		rep.Profile = st
		if err != nil {
			return rep, err
		}
		if err := saveIfUsed(pix, pdir); err != nil {
			rep.Warnings = append(rep.Warnings, "the profile index was not saved: "+err.Error())
		}
	}
	if s.project != nil && opts.Root != "" {
		st, err := SyncProject(ctx, s.project, opts.Root, opts.ProjectGate, opts.ProjectTokens)
		rep.Project = st
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			rep.Warnings = append(rep.Warnings, "the project index was not refreshed: "+err.Error())
		} else if err := saveIfUsed(s.project, s.projDir); err != nil {
			rep.Warnings = append(rep.Warnings, "the project index was not saved: "+err.Error())
		}
	}
	for _, w := range rep.Warnings {
		s.warn(w)
	}
	return rep, nil
}

func (s *Store) limits() (indexTokens, projectTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.IndexTokens, s.opts.ProjectTokens
}

// SetGates replaces the ingestion gates, so a store that outlives a change of
// provider classifies with the current one.
func (s *Store) SetGates(profile, project Gate) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.opts.ProfileGate, s.opts.ProjectGate = profile, project
	s.mu.Unlock()
}

// SetLimits replaces the corpus caps (zero or less keeps the config default)
// and trims any index that is now over its cap.
func (s *Store) SetLimits(indexTokens, projectTokens int) {
	if s == nil {
		return
	}
	if indexTokens <= 0 {
		indexTokens = config.DefaultKnowledgeIndexTokens
	}
	if projectTokens <= 0 {
		projectTokens = config.DefaultKnowledgeProjectTokens
	}
	s.mu.Lock()
	s.opts.IndexTokens, s.opts.ProjectTokens = indexTokens, projectTokens
	pix := s.profile
	s.mu.Unlock()
	pix.Fit(indexTokens)
	s.project.Fit(projectTokens)
}

// RefreshAsync starts a Refresh in the background unless the last one began
// less than minInterval ago. It reports whether it started one. A request with
// no interval (the profile just changed) that finds a refresh already running
// is not lost: it queues one more pass behind it, which sees the new profile.
func (s *Store) RefreshAsync(ctx context.Context, minInterval time.Duration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if s.running {
		if minInterval <= 0 {
			s.again = true
		}
		s.mu.Unlock()
		return false
	}
	if !s.lastRefresh.IsZero() && time.Since(s.lastRefresh) < minInterval {
		s.mu.Unlock()
		return false
	}
	s.running, s.lastRefresh = true, time.Now()
	s.mu.Unlock()
	go func() {
		for {
			_, _ = s.Refresh(ctx)
			s.mu.Lock()
			if s.again && ctx.Err() == nil {
				s.again = false
				s.mu.Unlock()
				continue
			}
			s.again, s.running = false, false
			s.mu.Unlock()
			return
		}
	}()
	return true
}

// saveIfUsed writes ix unless it is empty and has never been saved, so a
// project with nothing to index leaves no file behind.
func saveIfUsed(ix *Index, dir string) error {
	if len(ix.Docs()) == 0 {
		if _, err := os.Lstat(Path(dir)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return ix.Save(dir)
}

// AddSessionText indexes a file the user attached with `@`, for this session
// only: the text is chunked into the in-memory session index and dropped when
// the session ends. The file was already admitted by the attachment path, so
// there is no second classification. It shares the project cap with the
// .vulnetix output, which was indexed first.
func (s *Store) AddSessionText(ctx context.Context, name, source, text string) (Stats, error) {
	if s == nil {
		return Stats{}, nil
	}
	_, room := s.limits()
	if s.project != nil {
		room -= s.project.Tokens()
	}
	if room <= 0 {
		return Stats{Truncated: true, Skipped: 1}, ErrCapReached
	}
	// The session index holds other files too; the cap covers them all.
	other, addr := s.session.Tokens(), Address(SessionScope, name)
	for _, doc := range s.session.Docs() {
		if doc.Address == addr {
			other -= doc.Tokens
		}
	}
	return AddText(ctx, s.session, name, source, text, other+room)
}

// Docs lists the manifest of every index, for status output.
func (s *Store) Docs() map[string][]Doc {
	out := map[string][]Doc{}
	if s == nil {
		return out
	}
	for _, ix := range s.indexes() {
		out[ix.Name()] = append(out[ix.Name()], ix.Docs()...)
	}
	return out
}
