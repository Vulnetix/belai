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
		maxIter = 10
	}
	pool := nonce.New()
	if err := pool.SeedFromProvider(o.Cfg.BaseURL, o.Cfg.APIKey, 16); err != nil {
		if err := pool.Seed(16); err != nil {
			return nil, fmt.Errorf("seed nonce pool: %w", err)
		}
	}
	var openAITools []wire.OpenAITool
	var anthropicTools []wire.AnthropicToolDef
	if o.Registry != nil {
		for _, d := range o.Registry.Definitions() {
			openAITools = append(openAITools, d.OpenAITool())
			anthropicTools = append(anthropicTools, d.AnthropicTool())
		}
	}
	return &Session{
		cfg:            o.Cfg,
		client:         o.Client,
		registry:       o.Registry,
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

// Result is the outcome of a session run.
type Result struct {
	Text string
}

// Run executes the full pipeline including tool loop.
func (s *Session) Run(ctx context.Context, userPrompt string) (run.Result, error) {
	clean := sanitize.Sanitize(userPrompt)

	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))
	dec, err := pipe.Admit(clean, s.posture)
	if err != nil {
		return run.Result{SanitizedPrompt: clean}, err
	}
	if dec.Action != rolemanager.ActionProceed {
		return run.Result{SanitizedPrompt: clean}, &rolemanager.RefusalError{Sentinel: dec.Sentinel}
	}

	modeDec, err := rolemanager.Select(pipe.Classifier, rolemanager.ModeInput{Prompt: clean, GoalLimit: rolemanager.DefaultGoalPromptLengthLimit})
	if err != nil {
		return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel}, err
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

	turns := []run.Turn{{Role: "user", Content: clean}}

	for i := 0; i < s.maxIter; i++ {
		assistant, err := run.SendTurnsWithTools(s.cfg, system, turns, s.client, s.openAITools, s.anthropicTools)
		if err != nil {
			return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec}, err
		}

		if len(assistant.ToolCalls) == 0 {
			return run.Result{
				SanitizedPrompt:  clean,
				SecuritySentinel: dec.Sentinel,
				ModeDecision:     modeDec,
				Reply:            assistant.Text,
			}, nil
		}

		mismatchPol := s.mismatchPolicy()
		filtered, err := rolemanager.CheckToolCalls(assistant.ToolCalls, s.registry.Names(), mismatchPol)
		if err != nil {
			return run.Result{SanitizedPrompt: clean, SecuritySentinel: dec.Sentinel, ModeDecision: modeDec}, err
		}

		// Append assistant turn containing its tool_calls.
		turns = append(turns, run.Turn{
			Role:      "assistant",
			Content:   assistant.Text,
			ToolCalls: assistant.ToolCalls,
		})

		for _, call := range filtered {
			toolResult := s.executeCall(ctx, call)
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

func (s *Session) executeCall(ctx context.Context, call rolemanager.ToolCall) string {
	tool, ok := s.registry.Find(call.Name)
	if !ok {
		return fmt.Sprintf("tool result withheld: %q is not registered", call.Name)
	}

	if !modes.ToolAllowed(call.Name, call.Args, s.planMode) {
		return fmt.Sprintf("tool result withheld: %q is not allowed in plan mode", call.Name)
	}

	perm := s.perms.Evaluate(call.Name, tool.Subject(call.Args))
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
	dec, err := pipe.Process(res)
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
