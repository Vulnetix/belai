package tui

import (
	"context"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/kbgate"
	"github.com/vulnetix/belai/internal/tools"
)

// The TUI keeps one knowledge store for its life (docs/knowledge.md): the
// project's .vulnetix index and the session's @ files outlive every session
// rebuild, and the engaged agent's documents are swapped in and out of it as
// the user engages another agent. Plan and goal mode keep it too: the engaged
// agent is dormant there for tools and carrier, but its documents stay
// searchable, which is read access to the user's own reference material.

// knowledgeRefreshEvery bounds how often a session build starts a refresh.
const knowledgeRefreshEvery = 20 * time.Second

// knowledgeStore returns the App's store, opening it on first use.
func (a *App) knowledgeStore() *knowledge.Store {
	if a.knowStore != nil {
		return a.knowStore
	}
	index, project, _ := a.settings.KnowledgeLimits()
	a.knowStore = knowledge.Open(knowledge.Options{Root: a.workdir, IndexTokens: index, ProjectTokens: project})
	a.syncKnowledge()
	return a.knowStore
}

// syncKnowledge points the store at the engaged agent's documents, refreshes
// the ingestion gates and caps from the live state, and starts a refresh in the
// background when one is due. It is cheap and safe to call often.
func (a *App) syncKnowledge() {
	if a.knowStore == nil {
		return
	}
	index, project, _ := a.settings.KnowledgeLimits()
	a.knowStore.SetLimits(index, project)
	// The gates read the provider and posture as they are now, snapshotted
	// here on the UI goroutine so the background refresh never races them.
	cfg, client, cache, live := a.cfg, a.client, a.cache, a.live
	a.knowStore.SetGates(
		kbgate.New(cfg, client, cache, live, tools.KindRead),
		kbgate.New(cfg, client, cache, live, tools.KindRemote),
	)
	var prof *knowledge.Profile
	if p, ok := a.engagedProfile(); ok && p.ID != "" {
		if paths := p.KnowledgePaths(); len(paths) > 0 {
			prof = &knowledge.Profile{ID: p.ID, Name: p.Name, Paths: paths}
		}
	}
	changed := false
	a.knowStore.SetProfile(prof)
	if prof != nil && a.knowProfile != prof.ID {
		changed = true
	}
	if prof == nil {
		a.knowProfile = ""
	} else {
		a.knowProfile = prof.ID
	}
	interval := knowledgeRefreshEvery
	if changed {
		interval = 0
	}
	a.knowStore.RefreshAsync(context.Background(), interval)
}

// engagedKnowledge reports the engaged agent profile for tests and status.
func (a *App) engagedKnowledge() (agentprofile.AgentProfile, bool) {
	p, ok := a.engagedProfile()
	return p, ok && len(p.KnowledgePaths()) > 0
}

// indexAttachment adds an admitted @ file to the session's knowledge index. The
// file has already passed the attachment gate (confined to the roots, read
// through the same rules, classified or sanitised), and an image or a directory
// listing is not text worth indexing.
func (a *App) indexAttachment(att *attachment) {
	if att == nil || att.isDir || att.img != nil || att.body == "" {
		return
	}
	store := a.knowledgeStore()
	name := att.raw
	if name == "" {
		name = att.text
	}
	src := name
	if att.root != "" {
		src = att.root + "/" + name
	}
	_, _ = store.AddSessionText(context.Background(), name, src, att.body)
}

// knowledgeStoreForBuild is the store a session build takes. It opens the
// store on first use and gives a refresh the chance to start, so a session
// built after a scan sees the new output.
func (a *App) knowledgeStoreForBuild() *knowledge.Store {
	store := a.knowledgeStore()
	a.syncKnowledge()
	return store
}
