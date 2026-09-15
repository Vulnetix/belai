package agent

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/wire"
)

// Options configures a new agent session.
type Options struct {
	Cfg           run.Config
	Client        *http.Client
	Registry      *tools.Registry
	Perms         permissions.Settings
	Posture       posture.Policy
	PlanMode      bool
	MaxIterations int
	PromptOptions prompt.Options
	Workdir       string
	State         config.State
	Settings      config.Settings
}

// Session executes the tool loop for a single user prompt.
type Session struct {
	cfg            run.Config
	client         *http.Client
	registry       *tools.Registry
	perms          permissions.Settings
	posture        posture.Policy
	planMode       bool
	maxIter        int
	opts           prompt.Options
	workdir        string
	state          config.State
	settings       config.Settings
	pool           *nonce.Pool
	openAITools    []wire.OpenAITool
	anthropicTools []wire.AnthropicToolDef
}

// NewSession builds a session from options.
func NewSession(o Options) (*Session, error) {
	maxIter := o.MaxIterations
	if maxIter <= 0 {
		maxIter = o.Settings.Resilience.MaxIterationsOr(10)
	}
	pool := nonce.New()
	if err := pool.SeedFromProvider(o.Client, o.Cfg.BaseURL, o.Cfg.APIKey, 16); err != nil {
		if err := pool.Seed(16); err != nil {
			return nil, fmt.Errorf("seed nonce pool: %w", err)
		}
	}
	var openAITools []wire.OpenAITool
	var anthropicTools []wire.AnthropicToolDef
	reg := o.Registry
	if reg == nil {
		reg = tools.NewRegistry()
	}
	for _, d := range reg.Definitions() {
		openAITools = append(openAITools, d.OpenAITool())
		anthropicTools = append(anthropicTools, d.AnthropicTool())
	}
	return &Session{
		cfg:            o.Cfg,
		client:         o.Client,
		registry:       reg,
		perms:          o.Perms,
		posture:        o.Posture,
		planMode:       o.PlanMode,
		maxIter:        maxIter,
		opts:           o.PromptOptions,
		workdir:        o.Workdir,
		state:          o.State,
		settings:       o.Settings,
		pool:           pool,
		openAITools:    openAITools,
		anthropicTools: anthropicTools,
	}, nil
}

// TurnInput is the structured input for one agent turn. It carries the
// user's prompt plus any attachments and harness-level overrides that the
// TUI or CLI has already resolved.
type TurnInput struct {
	Prompt        string
	Attachments   []run.Attachment
	HasReferences bool
	ForceAgent    string
}

// Result is the outcome of a session run.
// (Kept as a type alias so callers continue to see run.Result.)
// Run executes the full pipeline including the tool loop, using the blocking
// transport. It is the CLI path and is byte-identical in behaviour to a
// drained RunStream.
func (s *Session) Run(ctx context.Context, userPrompt string) (run.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.run(ctx, nil, TurnInput{Prompt: userPrompt}, false, func(Event) {})
}

// run is the shared loop body for both transports. Order is identical to the
// pre-refactor Run: sanitize → Admit → Select → CarrierOptions → SealSystem →
// bounded loop → CheckToolCalls → executeCall.
func (s *Session) run(ctx context.Context, history []run.Turn, in TurnInput, streaming bool, emit func(Event)) (run.Result, error) {
	clean := sanitize.Sanitize(in.Prompt)

	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))
	dec, err := pipe.Admit(ctx, clean, s.posture)
	if err != nil {
		return run.Result{SanitizedPrompt: clean}, err
	}
	if dec.Action != rolemanager.ActionProceed {
		return run.Result{SanitizedPrompt: clean}, &rolemanager.RefusalError{Sentinel: dec.Sentinel}
	}

	modeDec, err := rolemanager.Select(ctx, pipe.Classifier, rolemanager.ModeInput{Prompt: clean, GoalLimit: rolemanager.DefaultGoalPromptLengthLimit, HasReferences: in.HasReferences})
	if err != nil {
		return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel}, err
	}

	if in.ForceAgent != "" {
		modeDec.Mode = modes.ModeAgent
		modeDec.AgentName = in.ForceAgent
		modeDec.AppendCarrier = true
		modeDec.Explore = false
	}

	opts, _ := CarrierOptions(s.workdir, modeDec, s.state, s.settings)
	if opts.Carrier == "" {
		opts = s.opts
	} else {
		opts.Caveman = s.opts.Caveman
	}

	system, err := run.SealSystem(s.cfg, s.pool, opts)
	if err != nil {
		return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec}, err
	}

	turns := append([]run.Turn{}, history...)
	turns = append(turns, run.Turn{Role: "user", Content: clean, Attachments: in.Attachments})

	for i := 0; i < s.maxIter; i++ {
		assistant, err := s.streamTurn(ctx, system, turns, streaming, emit)
		if err != nil {
			return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec}, err
		}

		if len(assistant.ToolCalls) == 0 {
			return run.Result{
				SanitizedPrompt:  clean,
				SecuritySentinel: dec.Sentinel,
				ModeDecision:     modeDec,
				Reply:            assistant.Text,
				Usage:            assistant.Usage,
			}, nil
		}

		mismatchPol := s.mismatchPolicy()
		filtered, err := rolemanager.CheckToolCalls(assistant.ToolCalls, s.registry.Names(), mismatchPol)
		if err != nil {
			return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec}, err
		}

		// Append assistant turn containing its tool_calls. Use the filtered
		// set so a PolicyStrip turn matches the tool turns that follow.
		turns = append(turns, run.Turn{
			Role:      "assistant",
			Content:   assistant.Text,
			ToolCalls: filtered,
		})

		for _, call := range filtered {
			if s.permissionDecision(call) == permissions.DecisionAsk {
				emit(Event{Kind: EventPermissionAskKind, AskName: call.Name})
			}
			emit(Event{Kind: EventToolStartKind, Tool: &call})
			toolResult := s.executeCall(ctx, call)
			emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolResult: toolResult})
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: call.ID,
				ToolName:   call.Name,
			})
		}
	}

	return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec},
		fmt.Errorf("max iterations (%d) reached", s.maxIter)
}

func (s *Session) mismatchPolicy() rolemanager.ToolCallMismatchPolicy {
	switch s.posture.Level(posture.ToolCallMismatch) {
	case posture.Warn:
		return rolemanager.PolicyStrip
	case posture.Ignore:
		return rolemanager.PolicyIgnore
	default:
		return rolemanager.PolicyAbort
	}
}

// decidePermission wraps the permissions layer and re-blocks unmatched calls
// when the permission_no_match posture gate is enforce (the legacy
// fail-closed behavior, restorable via preferences.yaml). Matched rules are
// unaffected by the gate: an explicit deny always blocks, an explicit allow
// always allows.
func (s *Session) decidePermission(tool, subject string) (permissions.Decision, string) {
	dec, rule := s.perms.Explain(tool, subject)
	if rule == "" && s.posture.Level(posture.PermissionNoMatch) == posture.Enforce {
		return permissions.DecisionBlock, ""
	}
	return dec, rule
}

func (s *Session) executeCall(ctx context.Context, call rolemanager.ToolCall) string {
	tool, ok := s.registry.Find(call.Name)
	if !ok {
		return fmt.Sprintf("tool result withheld: %q is not registered", call.Name)
	}

	if !modes.ToolAllowed(call.Name, call.Args, s.planMode) {
		return fmt.Sprintf("tool result withheld: %q is not allowed in plan mode", call.Name)
	}

	perm, _ := s.decidePermission(call.Name, tool.Subject(call.Args))
	switch perm {
	case permissions.DecisionBlock:
		return fmt.Sprintf("tool result withheld: permission denied for %q", call.Name)
	case permissions.DecisionAsk:
		if s.posture.Level(posture.PermissionAskNoTTY) == posture.Enforce {
			return fmt.Sprintf("tool result withheld: permission ask required for %q", call.Name)
		}
		// warn/ignore fall through to allow
	}

	res, err := tool.Execute(ctx, call.Args)
	if err != nil {
		return fmt.Sprintf("tool result withheld: execution error for %q: %v", call.Name, err)
	}

	if s.posture.Level(posture.ToolResultUnsafe) == posture.Ignore {
		return delimiters.Egress(res.Content, s.pool)
	}

	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))
	dec, err := pipe.Process(ctx, res)
	if err != nil {
		return fmt.Sprintf("tool result withheld: classifier error for %q: %v", call.Name, err)
	}

	if dec.Action == rolemanager.ActionProceed {
		return delimiters.Egress(dec.Content, s.pool)
	}

	if s.posture.Level(posture.ToolResultUnsafe) == posture.Warn {
		return fmt.Sprintf("tool result withheld: classified %s", dec.Sentinel)
	}

	// enforce — abort the whole turn. Since executeCall is called from the loop,
	// we use a sentinel string that the caller can detect if we later decide to
	// support partial failure. For now the agent loop treats any withheld as a
	// placeholder and continues; the strict abort is handled by refusing to
	// promote the unsafe content, which is what a placeholder does.
	return fmt.Sprintf("tool result withheld: classified %s", dec.Sentinel)
}
