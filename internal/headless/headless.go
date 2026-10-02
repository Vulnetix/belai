// Package headless builds top-level agent sessions outside the TUI: the CLI's
// -prompt run, each ACP session, and every fleet worker. One builder keeps
// them from drifting apart — the same registry, the same gates, the same
// fan-out ceiling — so a surface added for one reaches all three.
package headless

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentpool"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/kbgate"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tools"
)

// Params configures one headless session.
type Params struct {
	Cfg      run.Config
	Client   *http.Client
	Posture  posture.Policy
	Workdir  string
	Settings config.Settings
	PlanMode bool
	// SessionID is stamped on outbound calls and kanban writes.
	SessionID string
	// AllowAsk is true when someone can answer a permission ask (an ACP
	// editor); the CLI and fleet workers cannot.
	AllowAsk bool
	// AskDisabled resolves asks to allow. nil takes the settings' value.
	AskDisabled *bool
	// MCP is the connected MCP manager whose tools join the surface; nil for
	// none. The builder waits for its servers.
	MCP *mcp.Manager
	// Kanban and KanbanSource are the board and provenance; nil for none.
	Kanban       *kanban.Store
	KanbanSource *kanban.Source
	// Claim, with Kanban, makes this a fleet worker's session: the worker
	// kanban tools replace the ordinary ones.
	Claim *tools.WorkerClaim
	// Narrow, when set, narrows the full registry before the kanban tools
	// are added (a worker profile's tools allowlist).
	Narrow func(*tools.Registry) *tools.Registry
	// Deny adds permission rules on top of the settings' own. They are the
	// harness's, so a Permit rule may exempt a call from them; the user's own
	// Deny and Block rules are never exempted.
	Deny []string
	// Permit exempts calls from Deny: a worker profile's own declaration, such
	// as write access to a synced file, which a broad Deny rule would
	// otherwise refuse.
	Permit []string
	// Persona is a worker profile's text; see agent.Options.Persona.
	Persona string
	// Extra tools join after Narrow, so an allowlist cannot drop them: a
	// worker's PublishBranch, bound to its worktree.
	Extra []tools.Tool
	// SandboxMounts: agent.Options.SandboxMounts (a worker's git paths).
	SandboxMounts []sandbox.Mount
	SandboxEnv    []string
	// MaxIterations is the per-pass round budget; zero is the default.
	MaxIterations int
	// KnowledgeRoot is the trusted repository root whose .vulnetix output is
	// searchable through Grep, Glob and Read (docs/knowledge.md); empty means
	// Workdir. A worker sets it to its repository, never its worktree.
	KnowledgeRoot string
	// Facts are the engaged profile's facts (agentprofile.Facts). The cloud
	// tools read them as defaults and boundaries; nil means none.
	Facts map[string][]string
	// KnowledgeProfile is the agent profile whose documents are searchable, or
	// nil.
	KnowledgeProfile *knowledge.Profile
	// Knowledge is an already-open store; nil has the session open its own.
	Knowledge *knowledge.Store
}

// NewSession builds the session.
func NewSession(ctx context.Context, p Params) (*agent.Session, error) {
	caps := tools.DetectDefault()
	ix := repoindex.Scan(ctx, p.Workdir)
	// The full registry: read_only narrows agent-mode turns inside the session
	// (Options.ReadOnlyAgent) and never goal mode or an accepted plan.
	reg := tools.DefaultWithCaps(p.Workdir, false, caps, ix)
	reg.CloudHub().SetFacts(p.Facts)
	// A worker whose profile files handoffs under other repositories resolves
	// the name against this index: the checkouts beneath its directory.
	if p.Claim != nil && p.Claim.HandoffRepos {
		p.Claim.UseRepoIndex(ix)
	}
	// A headless session waits for the MCP servers to connect (or fail) so
	// their tools are on the surface from its first turn.
	if p.MCP != nil {
		p.MCP.Wait()
		reg = reg.With(p.MCP.Tools()...)
	}
	if p.Narrow != nil {
		reg = p.Narrow(reg)
	}
	if len(p.Extra) > 0 {
		reg = reg.With(p.Extra...)
	}
	// The global kanban board: search and update on every call, the loop and
	// wrap-up tools added per turn by the session — or, for a worker, the
	// tools bound to its claim.
	if p.Claim != nil {
		reg = reg.WithKanbanWorker(p.Kanban, p.KanbanSource, p.Claim)
	} else {
		reg = reg.WithKanban(p.Kanban, p.KanbanSource)
	}

	perms := permissions.From(p.Settings.Permissions.Allow, p.Settings.Permissions.Ask, p.Settings.Permissions.Deny).WithHarness(p.Deny, p.Permit)
	repoMap := repomap.Scan(ctx, p.Workdir)

	var promptOpts prompt.Options
	if p.Settings.Caveman != nil && *p.Settings.Caveman {
		promptOpts.Caveman = true
	}
	askDisabled := !p.Settings.AskPermissionEnabled()
	if p.AskDisabled != nil {
		askDisabled = *p.AskDisabled
	}

	kb := p.Knowledge
	if kb == nil {
		root := p.KnowledgeRoot
		if root == "" {
			root = p.Workdir
		}
		// An unattended session waits for the index, so its first search sees
		// the same documents its last one will.
		kb = kbgate.Open(ctx, kbgate.Setup{
			Cfg: p.Cfg, Client: p.Client, Levels: p.Posture, Settings: p.Settings,
			Root: root, Profile: p.KnowledgeProfile,
		}, true)
	}

	return agent.NewSession(agent.Options{
		Knowledge:     kb,
		WebPages:      true, // top-level session: WebFetch cache and index (docs/web-fetch.md)
		Cfg:           p.Cfg,
		Client:        p.Client,
		Registry:      reg,
		Perms:         perms,
		Posture:       p.Posture,
		PlanMode:      p.PlanMode,
		ReadOnlyAgent: p.Settings.ReadOnlyEnabled(),
		Workdir:       p.Workdir,
		Settings:      p.Settings,
		PromptOptions: promptOpts,
		SessionID:     p.SessionID,
		AllowAsk:      p.AllowAsk,
		Caps:          caps,
		RepoIndex:     ix,
		PlanSurface:   tools.PlanSurface{GuardrailsOff: !p.Settings.GuardrailsEnabled(), Perms: perms},
		AskDisabled:   askDisabled,
		// Top-level session: explore subagents may fan out from here. A
		// subagent sets this false so it can never fan out again.
		AllowExplore: true,
		// Top-level goal-mode prompts may run the unbounded pass loop; a
		// subagent never does.
		AllowPassLoop: true,
		ModeDetector:  run.NewModeDetector(p.Cfg),
		Jev:           run.NewJevJobs(p.Cfg, p.Settings.JevJobSet),
		RepoMap:       &repoMap,
		// The same settings-backed fan-out ceiling the TUI uses; without it
		// max_agents had no effect on the CLI.
		AgentPool: agentpool.New(p.Settings.Resilience.MaxAgentsOr(config.DefaultMaxAgents)),
		// Headless: live language servers are off, but fallback syntax checks
		// still run when enabled in settings.
		Diagnostics:   rolemanager.DiagnosticsGateFromSettings(p.Settings, reg.Cwd().Roots(), false),
		Persona:       p.Persona,
		SandboxMounts: p.SandboxMounts,
		SandboxEnv:    p.SandboxEnv,
		MaxIterations: p.MaxIterations,
	})
}

// Kanban opens the global board for a headless session, or returns nils
// when the kanban setting is off. A session with no id yet gets one for the
// provenance its writes carry. provWorkdir is the directory provenance is
// taken from — for a worker, the trusted repository root, never its
// worktree.
func Kanban(provWorkdir, sessionID string, settings config.Settings) (*kanban.Store, *kanban.Source) {
	if !settings.KanbanEnabled() {
		return nil, nil
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return nil, nil
	}
	if sessionID == "" {
		sessionID = session.MustID()
	}
	return store, kanban.NewSource(kanban.ProvenanceFor(provWorkdir, sessionID, HostID()))
}

// HostID is this install's sync host id, or "".
func HostID() string {
	if dir, err := config.GlobalDir(); err == nil {
		if id, err := sessionsync.HostID(dir); err == nil {
			return id
		}
	}
	return ""
}

// SyncClient returns the session-sync client when the kanban and sync
// settings are on and a usable Vulnetix CLI credential resolves, or nil.
func SyncClient(settings config.Settings, workdir string) *sessionsync.Client {
	return syncClient(settings, workdir)
}

// syncClient returns the session-sync client when the kanban and sync
// settings are on and a usable Vulnetix CLI credential resolves.
func syncClient(settings config.Settings, workdir string) *sessionsync.Client {
	if !settings.KanbanEnabled() || !settings.SyncEnabled() {
		return nil
	}
	header, err := credentials.VulnetixAuthHeader(workdir)
	if err != nil || sessionsync.UsableCredential(header) != nil {
		return nil
	}
	client, err := sessionsync.NewClient(sessionsync.BaseURL(os.Getenv("VULNETIX_WEB_URL")),
		func() (string, error) { return header, nil }, httpclient.Default())
	if err != nil {
		return nil
	}
	return client
}

// FlushKanban pushes the board's unsynced changes once, best effort. A
// headless run never pulls in the background; a fleet worker pulls with
// PullKanban.
func FlushKanban(settings config.Settings, workdir string) {
	if !settings.KanbanEnabled() || !settings.SyncEnabled() {
		return
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return
	}
	if out, err := store.Outbox(); err != nil || len(out) == 0 {
		return
	}
	client := syncClient(settings, workdir)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = kanban.NewSyncer(store, client, kanban.SyncOptions{}).Flush(ctx)
}

// PullKanban pushes local changes and pulls the website's, once, best
// effort, so a worker sees items filed on the web.
func PullKanban(ctx context.Context, store *kanban.Store, settings config.Settings, workdir string) {
	if store == nil {
		return
	}
	client := syncClient(settings, workdir)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s := kanban.NewSyncer(store, client, kanban.SyncOptions{})
	s.Sync(ctx)
}
