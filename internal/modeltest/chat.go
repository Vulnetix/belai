package modeltest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/mlclassify"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// ChatTarget is one chat model a selection uses.
type ChatTarget struct {
	// Role labels the step, e.g. "agent", "fast", "route mode_eval".
	Role string
	Cfg  run.Config
	// Status is run.Prepare's verdict for the provider; a missing credential
	// fails before any request is sent.
	Configured bool
	Missing    []string
	// Kind is the local template ("llama-server", "ollama") or "".
	Kind string
	// Sentinel also checks the model answers the security sentinel, which
	// every classifier LLM must.
	Sentinel bool
	// LaunchPort is where a chat llama-server the test launches listens.
	LaunchPort int
}

// ChatSteps is the ladder for a chat model.
func ChatSteps(t ChatTarget) []Step {
	label := t.Role
	var steps []Step
	steps = append(steps, Step{Name: label + " · credentials", Run: func(ctx context.Context, st *State) Outcome {
		if !t.Configured {
			what := strings.Join(t.Missing, ", ")
			if what == "" {
				what = "configuration"
			}
			return fail(fmt.Sprintf("%s is missing %s", t.Cfg.Provider, what), providersHint("configure "+t.Cfg.Provider+" in providers"))
		}
		return ok("%s is configured", t.Cfg.Provider)
	}})
	switch t.Kind {
	case "ollama":
		s := EnsureOllamaModel(t.Cfg.BaseURL, t.Cfg.Model)
		s.Name = label + " · " + s.Name
		steps = append(steps, s)
	case "llama-server":
		for _, s := range LlamaChatSteps(t.Cfg.BaseURL, t.Cfg.Model, t.LaunchPort) {
			s.Name = label + " · " + s.Name
			steps = append(steps, s)
		}
	}
	steps = append(steps, Step{Name: label + " · reply", Run: func(ctx context.Context, st *State) Outcome {
		return chatProbe(ctx, st, t.Cfg)
	}})
	if t.Sentinel {
		steps = append(steps, Step{Name: label + " · sentinel", Run: func(ctx context.Context, st *State) Outcome {
			return sentinelProbe(ctx, st, t.Cfg)
		}})
	}
	return steps
}

// chatProbe sends a minimal completion. A request the provider rejects for
// its reasoning or token options is retried without them.
func chatProbe(ctx context.Context, st *State, cfg run.Config) Outcome {
	probe := cfg
	probe.Effort = "none"
	probe.MaxTokens = 64
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	a, err := run.SendTurns(ctx, probe, "You are a connectivity check. Reply with the single word OK.", []run.Turn{{Role: "user", Content: "Reply OK."}}, st.Env.Client)
	if err != nil && isOptionRejection(err) {
		st.log("the provider rejected the reasoning/token options; retrying without them")
		probe.Effort, probe.MaxTokens = "", 0
		a, err = run.SendTurns(ctx, probe, "You are a connectivity check. Reply with the single word OK.", []run.Turn{{Role: "user", Content: "Reply OK."}}, st.Env.Client)
		if err == nil {
			return warn(fmt.Sprintf("%s replied in %s after dropping the reasoning/token options it rejects", cfg.Model, time.Since(start).Round(time.Millisecond)))
		}
	}
	if err != nil {
		return chatFailure(cfg, err)
	}
	if strings.TrimSpace(a.Text) == "" && strings.TrimSpace(a.Reasoning) == "" {
		return warn(fmt.Sprintf("%s answered with an empty reply", cfg.Model))
	}
	return ok("%s replied in %s", cfg.Model, time.Since(start).Round(time.Millisecond))
}

func isOptionRejection(err error) bool {
	var pe *run.ProviderError
	if !errors.As(err, &pe) || pe.Status != 400 {
		return false
	}
	b := strings.ToLower(pe.Body)
	for _, s := range []string{"reasoning", "effort", "max_to", "max_completion", "thinking", "unsupported parameter", "unknown parameter", "unrecognized"} {
		if strings.Contains(b, s) {
			return true
		}
	}
	return false
}

func chatFailure(cfg run.Config, err error) Outcome {
	var pe *run.ProviderError
	if errors.As(err, &pe) {
		body := oneLine(pe.Body, 160)
		switch {
		case pe.Status == 401 || pe.Status == 403:
			return fail(fmt.Sprintf("%s refused the credentials (HTTP %d)", cfg.Provider, pe.Status), providersHint("replace the "+cfg.Provider+" key in providers"), retryHint())
		case pe.Status == 404 || strings.Contains(strings.ToLower(pe.Body), "model_not_found") || strings.Contains(strings.ToLower(pe.Body), "does not exist"):
			return fail(fmt.Sprintf("%s does not serve model %q: %s", cfg.Provider, cfg.Model, body), Hint{Text: "pick another model from the list"})
		case pe.Status == 429:
			return fail(cfg.Provider+" is rate-limiting this key right now", retryHint())
		case pe.Status == 402:
			return fail(cfg.Provider+" reports no credit left on this account: "+body, providersHint("check the account or pick another provider"))
		case pe.Status >= 500:
			return fail(fmt.Sprintf("%s failed on its side (HTTP %d): %s", cfg.Provider, pe.Status, body), retryHint())
		case pe.Status == 0:
			return fail(fmt.Sprintf("could not reach %s: %s", cfg.Provider, oneLine(pe.Err.Error(), 140)), retryHint())
		}
		return fail(fmt.Sprintf("%s answered HTTP %d: %s", cfg.Provider, pe.Status, body), retryHint())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fail(cfg.Model+" did not answer within 60s", retryHint())
	}
	return fail(oneLine(err.Error(), 180), retryHint())
}

// sentinelProbe checks the model answers the security sentinel with a
// parseable token on benign content. A model that answers in prose would
// withhold every tool result, so this is a failure, not a warning.
func sentinelProbe(ctx context.Context, st *State, cfg run.Config) Outcome {
	gc := cfg
	gc.Classifier = run.ClassifierConfig{
		Provider: cfg.Provider, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
		Effort: "none", API: cfg.API, Auth: cfg.Auth, MaxTokens: run.ClassifierMaxTokens, Firewall: cfg.Firewall,
	}
	c := run.NewClassifierWithRetry(gc, st.Env.Client, nil)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := c.Classify(ctx, rolemanager.BuildClassifierPayload(benignState))
	if err != nil {
		return chatFailure(cfg, err)
	}
	s, perr := rolemanager.ParseSentinel(raw)
	if perr != nil {
		return fail(fmt.Sprintf("%s did not answer the security check with a verdict token (%q); it would hold back every tool result", cfg.Model, oneLine(raw, 60)),
			Hint{Text: "pick a model that follows short instructions, or turn reasoning off for the classifier"})
	}
	if s != rolemanager.SentinelSafe {
		return warn(fmt.Sprintf("%s flagged plain git output as %s; expect false positives", cfg.Model, s.Label()))
	}
	return ok("%s answered the security check (SAFE)", cfg.Model)
}

// PhaseSteps checks the local BERT gates of a models-kind classifier.
func PhaseSteps(sc run.SecurityClassifierConfig) []Step {
	if sc.Kind != "models" {
		return nil
	}
	var steps []Step
	if sc.Fallback != "" {
		steps = append(steps, Step{Name: "phases", Run: func(context.Context, *State) Outcome {
			return warn("no phase model in this build; the LLM sentinel classifies instead")
		}})
		return steps
	}
	steps = append(steps, Step{Name: "phases", Run: func(ctx context.Context, st *State) Outcome {
		if err := run.PreloadClassifier(sc); err != nil {
			return fail("an embedded phase model failed to load: "+oneLine(err.Error(), 160), retryHint())
		}
		var parts []string
		for i, p := range []*mlclassify.ModelConfig{sc.Phase1, sc.Phase2} {
			if p == nil {
				continue
			}
			parts = append(parts, fmt.Sprintf("phase %d %s (%s)", i+1, p.ID, p.Source))
			if p.Source == mlclassify.SourceHuggingFace && st.Env.HFToken == "" {
				return fail(fmt.Sprintf("phase %d runs on Hugging Face inference but no token is stored", i+1), hfTokenHint())
			}
		}
		if len(parts) == 0 {
			return ok("no local gate selected")
		}
		return ok("%s", strings.Join(parts, "; "))
	}})
	return steps
}

// Target is everything one /model selection uses.
type Target struct {
	Chats     []ChatTarget
	Phases    *run.SecurityClassifierConfig
	Decisions *run.DecisionsConfig
	// OpenRouterJev is set when the classifier is OpenRouter's hosted Jev.
	OpenRouterJev decisions.Decider
}

// Plan builds the steps for a target: chat models first, then phases, then
// the decision backend.
func Plan(t Target) []Step {
	var steps []Step
	seen := map[string]bool{}
	for _, c := range t.Chats {
		key := c.Cfg.Provider + "\x00" + c.Cfg.Model + "\x00" + c.Cfg.BaseURL + fmt.Sprint(c.Sentinel)
		if seen[key] {
			continue
		}
		seen[key] = true
		steps = append(steps, ChatSteps(c)...)
	}
	if t.Phases != nil {
		steps = append(steps, PhaseSteps(*t.Phases)...)
	}
	if d := t.Decisions; d != nil && d.On() {
		switch d.Backend {
		case decisions.BackendLocal:
			steps = append(steps, DecisionLocalSteps(d.Local, d.Timeout, d.MaxStateBytes)...)
		case decisions.BackendSystemOne:
			steps = append(steps, SystemOneSteps(SystemOneTarget{Name: d.Provider, BaseURL: d.BaseURL, Path: d.Path, Key: d.Key, Timeout: d.Timeout})...)
		}
	}
	if t.OpenRouterJev != nil {
		steps = append(steps, DeciderSteps("jev · openrouter", t.OpenRouterJev)...)
	}
	return steps
}
