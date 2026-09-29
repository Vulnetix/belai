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
	Key     func() (string, error)
	Local   decisions.LocalModel
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
			Name:    d.Provider,
			Model:   d.Model,
			BaseURL: d.BaseURL,
			Path:    d.Path,
			Key:     d.Key,
			Client:  client,
			Timeout: d.Timeout,
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
