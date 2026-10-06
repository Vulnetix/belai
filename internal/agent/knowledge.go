package agent

import (
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/tools"
)

// knowledgeAdapter is the tools.Knowledge a session installs in its registry's
// hub (docs/knowledge.md). It holds the three limits that belong to the
// session rather than the store: the per-search token cap from the user's
// settings, the session's permission rules, and the workspace roots a rule's
// path may be relative to.
//
// Every chunk behind it was sanitised and classified when it was ingested, so
// a search calls no model. What the adapter adds is the deny filter: a source
// path that a Read deny rule blocks never surfaces through retrieval either.
type knowledgeAdapter struct {
	set    *knowledge.Set
	limit  int
	perms  func() permissions.Settings
	roots  func() []string
	denied func(source string) bool // test seam; nil uses the permission rules
}

// Search returns the best passages within the per-search cap.
func (a *knowledgeAdapter) Search(query string) []tools.KnowledgeHit {
	hits := a.set.Search(query, a.limit, func(h knowledge.Hit) bool { return !a.deny(h.Source) })
	out := make([]tools.KnowledgeHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, tools.KnowledgeHit{Address: h.Address, Source: h.Source, Start: h.Start, End: h.End, Text: h.Text, Score: h.Score})
	}
	return out
}

// Match returns the document addresses that match and are not denied.
func (a *knowledgeAdapter) Match(match func(address string) bool) []string {
	return a.set.Match(func(address, source string) bool { return !a.deny(source) && match(address) })
}

// deny reports whether a Read deny rule blocks source, given as the absolute
// path and, under a workspace root, the root-relative one, since a rule may be
// written either way.
func (a *knowledgeAdapter) deny(source string) bool {
	if a.denied != nil {
		return a.denied(source)
	}
	if source == "" {
		return false
	}
	perms := a.perms()
	subjects := []string{source}
	if a.roots != nil {
		for _, root := range a.roots() {
			if rel, err := filepath.Rel(root, source); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				subjects = append(subjects, filepath.ToSlash(rel))
			}
		}
	}
	for _, subj := range subjects {
		if dec, rule := perms.Explain("Read", subj); rule != "" && dec == permissions.DecisionBlock {
			return true
		}
	}
	return false
}

// installKnowledge points the registry's file tools at the session's store.
// A nil store leaves them filesystem-only.
func (s *Session) installKnowledge(store *knowledge.Store) {
	if store == nil {
		return
	}
	hub := s.registry.KnowledgeHub()
	if hub == nil {
		return
	}
	_, _, limit := s.settings.KnowledgeLimits()
	hub.Set(&knowledgeAdapter{
		set:   store.Set(),
		limit: limit,
		perms: func() permissions.Settings { return s.perms },
		roots: func() []string {
			if cwd := s.registry.Cwd(); cwd != nil {
				return cwd.Roots()
			}
			return []string{s.workdir}
		},
	})
}
