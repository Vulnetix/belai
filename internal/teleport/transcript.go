package teleport

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Bounds on a transcript taken from the backend. The backend is not trusted, so
// a transcript larger than any session Belai writes is refused rather than read.
const (
	// maxEntries is the most lines a teleported session may have.
	maxEntries = 200_000
	// maxEntryBytes is the largest content or meta of one line, the same as the
	// session reader's own line cap.
	maxEntryBytes = 16 << 20
	// maxTotalBytes is the most a whole transcript may hold.
	maxTotalBytes = 512 << 20
	// maxMetaDepth bounds how deep a line's meta may nest.
	maxMetaDepth = 32
)

var (
	// entryIDShape is an entry id: Belai writes UUIDs, and the backend keeps up
	// to 64 characters of identifier.
	entryIDShape = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	typeShape    = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,47}$`)
	roleShape    = regexp.MustCompile(`^[a-z_]{0,32}$`)
)

// verify checks that entries are the transcript the teleport froze: every line
// from 0 through snapshot, once each, in order. This is the "data sync
// verified" step: a gap or a short page is a refusal, never a partial session.
func verify(entries []sessionsync.Entry, snapshot int64) error {
	if snapshot < 0 {
		return errors.New("the backend holds no lines for that session")
	}
	if snapshot+1 > maxEntries {
		return fmt.Errorf("the session has more than %d lines, which is more than a teleport takes", maxEntries)
	}
	if int64(len(entries)) != snapshot+1 {
		return fmt.Errorf("the transcript has %d lines where the backend froze %d", len(entries), snapshot+1)
	}
	seen := make(map[string]struct{}, len(entries))
	total := 0
	for i, e := range entries {
		if e.Seq != int64(i) {
			return fmt.Errorf("the transcript skips from line %d to %d", i-1, e.Seq)
		}
		if !entryIDShape.MatchString(e.ID) {
			return fmt.Errorf("line %d has no usable id", i)
		}
		if _, dup := seen[e.ID]; dup {
			return fmt.Errorf("line %d repeats an earlier id", i)
		}
		seen[e.ID] = struct{}{}
		if !typeShape.MatchString(e.Type) || !roleShape.MatchString(e.Role) {
			return fmt.Errorf("line %d has a type or role Belai does not write", i)
		}
		if len(e.Content) > maxEntryBytes || len(e.Meta) > maxEntryBytes {
			return fmt.Errorf("line %d is larger than a line Belai reads", i)
		}
		total += len(e.Content) + len(e.Meta) + 128
		if total > maxTotalBytes {
			return errors.New("the transcript is larger than a teleport takes")
		}
	}
	return nil
}

// build turns the verified lines into the new session's entries: a fresh
// session_meta first, then every other line with its text cleaned. The origin's
// own session_meta lines are not carried; what they said that a session may
// keep (mode, active profile) is read with session.LatestMeta and written into
// the new one, and the working directory, plan, goal and repository facts are
// this host's to set.
//
// The chain is rebuilt around the dropped lines: a line whose parent was a
// session_meta line takes the nearest kept ancestor, or the new meta, so the
// file has one root and a branch the origin navigated to stays a branch.
func build(entries []sessionsync.Entry, meta session.Meta, keepProfile func(name string) bool) ([]session.Entry, session.Meta, error) {
	parsed := make([]session.Entry, 0, len(entries))
	for _, e := range entries {
		var m map[string]any
		if len(e.Meta) > 0 {
			if err := json.Unmarshal(e.Meta, &m); err != nil {
				return nil, meta, fmt.Errorf("line %d has meta that is not an object", e.Seq)
			}
			if !shallow(m, 0) {
				return nil, meta, fmt.Errorf("line %d has meta nested deeper than Belai writes", e.Seq)
			}
			cleanMeta(m)
		}
		parsed = append(parsed, session.Entry{
			ID: e.ID, ParentID: e.ParentID, Type: e.Type, Role: e.Role,
			Content: sanitize.Text(e.Content), Timestamp: e.Timestamp, Meta: m, SubagentID: sanitize.Ident(e.SubagentID, 64),
		})
	}
	origin, _ := session.LatestMeta(parsed)
	meta.Mode = origin.Mode
	if origin.ActiveProfile != "" && keepProfile != nil && keepProfile(origin.ActiveProfile) {
		meta.ActiveProfile = origin.ActiveProfile
	}

	root := meta.ToEntry("")
	parentOf := make(map[string]string, len(parsed))
	dropped := make(map[string]bool)
	for _, e := range parsed {
		parentOf[e.ID] = e.ParentID
		if e.Type == session.EntryTypeSessionMeta {
			dropped[e.ID] = true
		}
	}
	out := make([]session.Entry, 0, len(parsed)+1)
	out = append(out, root)
	for _, e := range parsed {
		if dropped[e.ID] {
			continue
		}
		p := e.ParentID
		for hops := 0; dropped[p] && hops <= len(parsed); hops++ {
			p = parentOf[p]
		}
		if p == "" || dropped[p] {
			p = root.ID
		}
		e.ParentID = p
		out = append(out, e)
	}
	// ToEntry stamped CreatedAt and the schema; hand them back.
	written, _ := session.MetaFromEntry(root)
	return out, written, nil
}

// shallow reports whether a decoded JSON value nests no deeper than
// maxMetaDepth.
func shallow(v any, depth int) bool {
	if depth > maxMetaDepth {
		return false
	}
	switch t := v.(type) {
	case map[string]any:
		for _, x := range t {
			if !shallow(x, depth+1) {
				return false
			}
		}
	case []any:
		for _, x := range t {
			if !shallow(x, depth+1) {
				return false
			}
		}
	}
	return true
}

// cleanMeta cleans every string in a line's meta the way its content is
// cleaned. Keys are left alone: they are Belai's own names, and a key that is
// not is data a reader looks up by name and never runs.
func cleanMeta(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok {
				t[k] = sanitize.Text(s)
				continue
			}
			cleanMeta(x)
		}
	case []any:
		for i, x := range t {
			if s, ok := x.(string); ok {
				t[i] = sanitize.Text(s)
				continue
			}
			cleanMeta(x)
		}
	}
}
