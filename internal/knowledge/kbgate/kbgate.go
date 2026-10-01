// Package kbgate builds the knowledge.Gate the harness uses at ingestion: a
// chunk is classified once, when it is indexed, so a search later is only a
// lookup over admitted text.
package kbgate

import (
	"context"
	"net/http"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// Levels reads the effective posture. *posture.Live and posture.Policy both
// satisfy it; a live one lets a change of posture take effect on the next
// chunk.
type Levels interface {
	Level(posture.Gate) posture.Level
}

// New returns a Gate that classifies a chunk as kind (tools.KindRead for a
// profile's documents, tools.KindRemote for a scanner's third-party text)
// through the same pipeline a tool result takes. The level is checked before
// the classifier is called, never after: with guardrails off a verdict could
// not change the outcome, so nothing is sent. A classifier error or a verdict
// other than proceed does not admit the chunk, and an error is returned so the
// document is not indexed at all (fail closed).
func New(cfg run.Config, client *http.Client, cache *rolemanager.Cache, levels Levels, kind tools.Kind) knowledge.Gate {
	return func(ctx context.Context, text string) (bool, error) {
		if levels != nil && levels.Level(posture.ToolResultUnsafe) == posture.Ignore {
			return true, nil
		}
		dec, err := run.NewPipeline(cfg, client, cache).Process(ctx, tools.Result{Kind: kind, Content: text})
		if err != nil {
			return false, err
		}
		return dec.Action == rolemanager.ActionProceed, nil
	}
}

// Setup is what a session builder knows when it opens a Store.
type Setup struct {
	Cfg      run.Config
	Client   *http.Client
	Cache    *rolemanager.Cache
	Levels   Levels
	Settings config.Settings
	// Root is the trusted repository root whose .vulnetix output is indexed;
	// empty means no project index. A worker passes its repository, never its
	// worktree.
	Root string
	// Profile is the agent profile whose documents are indexed, or nil.
	Profile *knowledge.Profile
	// CopyOutside places copies of the profile's documents that live outside
	// the project under Root/.vulnetix/knowledge (knowledge.Options).
	CopyOutside bool
}

// Open builds the session's Store: the caps come from the user's settings and
// each part's chunks are classified through its own gate (a profile's
// documents as file text, the scanner's output as third-party text). With
// nothing to cover it returns nil. Open loads what is on disk at once. With
// wait the persistent indexes are then brought up to date before it returns,
// which an unattended session wants; without it the refresh runs in the
// background and a search sees whatever has been indexed so far. A cancelled
// ctx stops a refresh.
func Open(ctx context.Context, s Setup, wait bool) *knowledge.Store {
	if s.Root == "" && s.Profile == nil {
		return nil
	}
	index, project, _ := s.Settings.KnowledgeLimits()
	store := knowledge.Open(knowledge.Options{
		Root:          s.Root,
		Profile:       s.Profile,
		IndexTokens:   index,
		ProjectTokens: project,
		ProfileGate:   New(s.Cfg, s.Client, s.Cache, s.Levels, tools.KindRead),
		ProjectGate:   New(s.Cfg, s.Client, s.Cache, s.Levels, tools.KindRemote),
		CopyOutside:   s.CopyOutside,
	})
	if wait {
		_, _ = store.Refresh(ctx)
		return store
	}
	go func() { _, _ = store.Refresh(ctx) }()
	return store
}
