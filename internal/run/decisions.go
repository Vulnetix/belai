package run

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// DecisionsConfig is a resolved decision backend other than OpenRouter's
// hosted Jev: the local decision model or a self-hosted Jev server. When set
// on the classifier, it answers every Jev job (security, intent detection,
// routing). The zero value means none.
type DecisionsConfig struct {
	Backend  decisions.Backend
	Provider string
	Model    string
	// BaseURL is the self-hosted server's base URL (systemone only; the
	// local server's address is read from the decision server's state on
	// every call).
	BaseURL string
	Path    string
	// SendModel sends Model in the request body (TypeSafe's hosted API takes
	// it; a self-hosted server may reject fields it does not know).
	SendModel bool
	Key       func() (string, error)
	Local     decisions.LocalModel
	// Timeout and MaxStateBytes come from classifier.decision; zero keeps
	// the backend default.
	Timeout       time.Duration
	MaxStateBytes int
}

// On reports whether a local or self-hosted decision backend is configured.
func (d DecisionsConfig) On() bool { return d.Backend != "" }

// Label names the backend for the security trace and the /model rows.
func (d DecisionsConfig) Label() string { return d.Provider + "/" + d.Model }

// NewDecider builds the transport for a resolved backend.
func (d DecisionsConfig) NewDecider(client *http.Client) decisions.Decider {
	switch d.Backend {
	case decisions.BackendLocal:
		sup := decisionserver.Shared(d.Local)
		return &decisions.Llama{
			Model:         d.Local,
			Client:        client,
			Timeout:       d.Timeout,
			MaxStateBytes: d.MaxStateBytes,
			Resolve: func() string {
				if h := sup.Handle(); h != nil {
					return h.BaseURL
				}
				return decisionserver.BaseURL()
			},
			OnConnRefused: sup.Recover,
		}
	case decisions.BackendSystemOne:
		return &decisions.SystemOne{
			Name:      d.Provider,
			Model:     d.Model,
			SendModel: d.SendModel,
			BaseURL:   d.BaseURL,
			Path:      d.Path,
			Key:       d.Key,
			Client:    client,
			Timeout:   d.Timeout,
		}
	}
	return nil
}

// resolveDecisions recognises a classifier provider that is a decision
// backend and resolves it without ever preparing a chat config for it. ok is
// false when the provider is an ordinary chat provider (or OpenRouter's Jev,
// which keeps its own path).
func resolveDecisions(cls *config.ClassifierSettings, src CredentialSource) (DecisionsConfig, bool, error) {
	if cls == nil || cls.Provider == "" {
		return DecisionsConfig{}, false, nil
	}
	kind := ""
	var prof struct {
		baseURL, path string
		models        []string
	}
	if ps, ok := src.(ProviderSource); ok && cls.Provider != decisions.LocalProvider {
		if p, ok := ps.Profile(cls.Provider); ok {
			kind = p.Kind
			prof.baseURL, prof.path, prof.models = p.BaseURL, p.DecisionPath, p.Models
		}
	}
	backend, ok := decisions.BackendOf(cls.Provider, kind, cls.Model)
	if !ok || backend == decisions.BackendOpenRouter {
		return DecisionsConfig{}, false, nil
	}
	out := DecisionsConfig{
		Backend:       backend,
		Provider:      cls.Provider,
		Model:         cls.Model,
		Timeout:       time.Duration(cls.Decision.TimeoutMS) * time.Millisecond,
		MaxStateBytes: cls.Decision.MaxStateBytes,
	}
	switch backend {
	case decisions.BackendLocal:
		m, ok := decisions.LocalModelByID(cls.Model)
		if !ok {
			return DecisionsConfig{}, true, fmt.Errorf("classifier.model %q is not a local decision model (want %s)", cls.Model, localModelIDs())
		}
		out.Local = m
	case decisions.BackendSystemOne:
		if cls.Provider == decisions.TypeSafeProvider {
			// TypeSafe's hosted API: a fixed origin and path, a model the
			// service names, and a key that is required.
			out.BaseURL, out.Path = decisions.TypeSafeBaseURL, decisions.DefaultSystemOnePath
			if out.Model == "" {
				out.Model = decisions.TypeSafeDefaultModel
			}
			out.SendModel = true
			out.Key = func() (string, error) {
				if src == nil {
					return "", fmt.Errorf("%s is not set", decisions.TypeSafeKeyEnv)
				}
				v, _, _ := src.Lookup(decisions.TypeSafeProvider, "api_key")
				v = strings.TrimSpace(v)
				if v == "" {
					return "", fmt.Errorf("%s is not set", decisions.TypeSafeKeyEnv)
				}
				return v, nil
			}
			return out, true, nil
		}
		if err := config.ValidJevURL(prof.baseURL); err != nil {
			return DecisionsConfig{}, true, fmt.Errorf("provider %q: %w", cls.Provider, err)
		}
		out.BaseURL, out.Path = prof.baseURL, prof.path
		if out.Model == "" && len(prof.models) > 0 {
			out.Model = prof.models[0]
		}
		name := cls.Provider
		out.Key = func() (string, error) {
			if src == nil {
				return "", nil
			}
			v, _, _ := src.Lookup(name, "api_key")
			return strings.TrimSpace(v), nil
		}
	}
	return out, true, nil
}

func localModelIDs() string {
	ids := make([]string, 0, len(decisions.LocalModels))
	for _, m := range decisions.LocalModels {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, " or ")
}

// jevClientFor returns the Jev client for mode detection and routing: the
// configured decision backend when there is one, else OpenRouter's Jev with
// token (nil when there is neither).
func jevClientFor(cfg Config, token func() (string, error), client *http.Client) *jev.Client {
	if d := cfg.ClassifierOrDefault().Decisions; d.On() {
		return jev.NewWith(d.NewDecider(client))
	}
	if token == nil {
		return nil
	}
	return jev.New(token)
}

// isDecisionsTarget reports whether a resolved provider config is any
// decision backend, which must never be asked to chat.
func isDecisionsTarget(provider, kind, model string) bool {
	_, ok := decisions.BackendOf(provider, kind, model)
	return ok
}

// WarmDecisions starts loading the local decision server in the background
// when the classifier names one, so the first checks find it ready. It never
// downloads. The returned notice is harness text for the user when the
// server cannot start (weights missing, no llama-server); checks then use the
// agent-model fallback.
func WarmDecisions(cfg Config) string {
	d := cfg.ClassifierOrDefault().Decisions
	if d.Backend != decisions.BackendLocal {
		return ""
	}
	if decisionserver.ModelPath(d.Local) == "" {
		return decisionserver.Describe(decisionserver.ErrWeightsMissing)
	}
	decisionserver.Shared(d.Local).Recover()
	return ""
}

// NewJevJobs returns the runner for the Jev relevance jobs (bash swap and the
// others in docs/jev-jobs.md), or nil when no decision backend is configured,
// so with none every job is off. on reports each job's own switch from the
// caller's settings; nil means all on. The client keeps a session score
// cache, so repeated questions cost nothing.
func NewJevJobs(cfg Config, on func(config.JevJob) bool) *jev.Jobs {
	var client *jev.Client
	switch {
	case cfg.ClassifierOrDefault().Decisions.On():
		client = jevClientFor(cfg, nil, nil)
	case cfg.Routing.JevToken != nil &&
		cfg.Classifier.Provider == "openrouter" && strings.HasPrefix(cfg.Classifier.Model, "typesafe/jev"):
		client = jev.New(cfg.Routing.JevToken)
	}
	if client == nil {
		return nil
	}
	client.SetScoreCache(jev.NewScoreCache(0))
	return &jev.Jobs{Client: client, On: on}
}
