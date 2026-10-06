package rc

import (
	"os"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The knowledge catalogue the daemon advertises, so the Library's Documents view
// can show what each host's RAG holds and what differs from the library. It is
// read through the read-only catalogue (knowledge.LoadProfile, LoadProject) and
// carries facts only: each document's knowledge address, size, SHA-256 and the
// labels and topics the harness tagged it with from its fixed tables. No passage
// text, no label chunk and no index file leaves the host (AGENTS.md, Knowledge).
//
// An index file is loaded again only when its size or modification time
// changes, so a heartbeat that re-reads the inventory does not reload it.

var (
	knowledgeSHARE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	knowledgeLabelRE = regexp.MustCompile(`^[a-z0-9_+#.-]{1,24}:[A-Za-z0-9_+#./-]{1,64}$`)
	knowledgeWordRE  = regexp.MustCompile(`^[a-z0-9_+#.-]{1,32}$`)
)

// Caps on one document's tags.
const (
	maxKnowledgeLabels = 24
	maxKnowledgeTopics = 8
)

type knowledgeCacheEntry struct {
	size  int64
	mod   time.Time
	index sessionsync.RCKnowledge
}

var knowledgeCache sync.Map // index file path -> knowledgeCacheEntry

// localKnowledge lists the indexes of this host's stored agent profiles and of
// the directories the daemon offers, within the advertisement's caps.
func localKnowledge(dirs []Dir) []sessionsync.RCKnowledge {
	var out []sessionsync.RCKnowledge
	docs := 0
	add := func(ix sessionsync.RCKnowledge, ok bool) {
		if !ok || len(ix.Docs) == 0 || len(out) >= sessionsync.MaxRCKnowledgeIndexes || docs >= sessionsync.MaxRCKnowledgeDocs {
			return
		}
		if room := sessionsync.MaxRCKnowledgeDocs - docs; len(ix.Docs) > room {
			ix.Docs = ix.Docs[:room]
		}
		docs += len(ix.Docs)
		out = append(out, ix)
	}
	if list, err := agentprofile.List(); err == nil {
		for _, p := range list {
			if p.Builtin || !agentprofile.ValidID(p.ID) {
				continue
			}
			dir, err := config.ProfileKnowledgeDir(p.ID)
			if err != nil {
				continue
			}
			add(cachedKnowledge(dir, func() knowledge.IndexInfo { return knowledge.LoadProfile(p.ID, p.Name) },
				sessionsync.RCKnowledge{Scope: knowledge.ScopeProfile, Key: p.ID, Name: sanitize.Line(p.Name, 128)}))
		}
	}
	for _, d := range dirs {
		dir, err := config.ProjectKnowledgeDir(d.Path)
		if err != nil {
			continue
		}
		add(cachedKnowledge(dir, func() knowledge.IndexInfo { return knowledge.LoadProject(d.Path) },
			sessionsync.RCKnowledge{Scope: knowledge.ScopeProject, Name: sanitize.Line(d.Name, 128), Root: d.Path}))
	}
	return out
}

// cachedKnowledge returns the facts of the index in dir, loading it only when
// the file changed. ok is false when there is no usable index.
func cachedKnowledge(dir string, load func() knowledge.IndexInfo, head sessionsync.RCKnowledge) (sessionsync.RCKnowledge, bool) {
	path := knowledge.Path(dir)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		knowledgeCache.Delete(path)
		return head, false
	}
	if v, ok := knowledgeCache.Load(path); ok {
		e := v.(knowledgeCacheEntry)
		if e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
			ix := e.index
			ix.Scope, ix.Key, ix.Name, ix.Root = head.Scope, head.Key, head.Name, head.Root
			return ix, true
		}
	}
	info := load()
	if info.Err != nil || info.Index == nil {
		return head, false
	}
	head.Docs = knowledgeDocs(info.Index.Docs())
	knowledgeCache.Store(path, knowledgeCacheEntry{size: fi.Size(), mod: fi.ModTime(), index: head})
	return head, true
}

// knowledgeDocs reduces manifest entries to the facts the advertisement carries.
func knowledgeDocs(docs []knowledge.Doc) []sessionsync.RCKnowledgeDoc {
	out := make([]sessionsync.RCKnowledgeDoc, 0, len(docs))
	for _, d := range docs {
		if !knowledge.IsAddress(d.Address) || !knowledgeSHARE.MatchString(d.SHA) {
			continue
		}
		doc := sessionsync.RCKnowledgeDoc{
			Address: sanitize.Line(d.Address, 512), Bytes: max(d.Size, 0), SHA256: d.SHA,
			Labels: []string{}, Topics: []string{},
		}
		if knowledgeWordRE.MatchString(d.Tags.Kind) {
			doc.Kind = d.Tags.Kind
		}
		if knowledgeWordRE.MatchString(d.Tags.Lang) {
			doc.Lang = d.Tags.Lang
		}
		for _, l := range d.Tags.Labels {
			if len(doc.Labels) < maxKnowledgeLabels && knowledgeLabelRE.MatchString(l) {
				doc.Labels = append(doc.Labels, l)
			}
		}
		for _, t := range d.Tags.Topics {
			if len(doc.Topics) < maxKnowledgeTopics && knowledgeWordRE.MatchString(t.ID) {
				doc.Topics = append(doc.Topics, t.ID)
			}
		}
		out = append(out, doc)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Address < out[b].Address })
	return out
}
