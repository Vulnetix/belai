package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ProjectSessions groups every session on disk under one project key.
type ProjectSessions struct {
	Key      Key
	Workdir  string // "" when unrecoverable
	Label    string // Workdir, else Key.Project() + " (path unknown)"
	Current  bool
	Sessions []SessionInfo // ModTime desc
	Latest   int64
}

// AllSessions groups every session on disk by project. The group matching
// current sorts first; the rest follow by most-recent activity.
func (s *Store) AllSessions(current Key) ([]ProjectSessions, error) {
	keys, err := s.Keys()
	if err != nil {
		return nil, err
	}
	var groups []ProjectSessions
	for _, k := range keys {
		sessions, err := s.SessionsIn(k)
		if err != nil {
			return nil, err
		}
		g := ProjectSessions{
			Key:      k,
			Workdir:  s.projectWorkdir(k, sessions),
			Sessions: sessions,
			Current:  k == current,
		}
		if g.Workdir != "" {
			g.Label = g.Workdir
		} else {
			g.Label = k.Project() + " (path unknown)"
		}
		for _, si := range sessions {
			if si.ModTime > g.Latest {
				g.Latest = si.ModTime
			}
		}
		groups = append(groups, g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Current != groups[j].Current {
			return groups[i].Current
		}
		return groups[i].Latest > groups[j].Latest
	})
	return groups, nil
}

// projectWorkdir derives a verified workdir for a key from the first session
// in the directory that records one. The recorded cwd is accepted only when
// WorkdirKey(cwd) == key, so a session file copied between machines degrades
// to "path unknown" rather than mislabelling the row.
func (s *Store) projectWorkdir(k Key, sessions []SessionInfo) string {
	for _, si := range sessions {
		entries, err := s.ReadFrom(k, si.ID)
		if err != nil {
			continue
		}
		if m, ok := LatestMeta(entries); ok && m.Cwd != "" {
			if WorkdirKey(m.Cwd) == string(k) {
				return m.Cwd
			}
			return "" // recorded cwd fails the integrity check
		}
	}
	return ""
}

// ResolveAnywhere resolves a full or partial id, preferring prefer's project
// then scanning all of them. Ambiguity errors list each candidate with its
// project so the user can disambiguate.
func (s *Store) ResolveAnywhere(prefer Key, idOrPrefix string) (Key, string, error) {
	if idOrPrefix == "" {
		return "", "", errors.New("session id is empty")
	}
	// Prefer the current project.
	if id, err := s.resolveIn(prefer, idOrPrefix); err == nil {
		return prefer, id, nil
	} else if !isNotFound(err) && !isAmbiguous(err) {
		return "", "", err
	} else if isAmbiguous(err) {
		return "", "", s.anywhereAmbiguity(prefer, idOrPrefix)
	}

	keys, err := s.Keys()
	if err != nil {
		return "", "", err
	}
	type candidate struct {
		key Key
		id  string
	}
	var candidates []candidate
	for _, k := range keys {
		if k == prefer {
			continue
		}
		id, err := s.resolveIn(k, idOrPrefix)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{key: k, id: id})
	}
	switch len(candidates) {
	case 0:
		return "", "", fmt.Errorf("no session matching %q", idOrPrefix)
	case 1:
		return candidates[0].key, candidates[0].id, nil
	default:
		var b strings.Builder
		for _, c := range candidates {
			fmt.Fprintf(&b, "%s  %s\n", shortSessionID(c.id), c.key.Project())
		}
		return "", "", fmt.Errorf("ambiguous session prefix %q matches %d sessions:\n%s", idOrPrefix, len(candidates), strings.TrimRight(b.String(), "\n"))
	}
}

// anywhereAmbiguity reports ambiguity within the preferred project, listing
// each candidate with its project (the preferred project name).
func (s *Store) anywhereAmbiguity(prefer Key, idOrPrefix string) error {
	matches, err := s.lookupIn(prefer, idOrPrefix)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, id := range matches {
		fmt.Fprintf(&b, "%s  %s\n", shortSessionID(id), prefer.Project())
	}
	return fmt.Errorf("ambiguous session prefix %q matches %d sessions:\n%s", idOrPrefix, len(matches), strings.TrimRight(b.String(), "\n"))
}

func isNotFound(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "no session matching")
}

func isAmbiguous(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "ambiguous session prefix")
}
