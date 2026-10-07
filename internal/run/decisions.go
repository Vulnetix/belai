package run

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// DecisionsConfig is a resolved decision backend other than OpenRouter's
// hosted Jev: the local decision model, Strands Decider-2B on this machine,
// or a server speaking /v1/systemone (TypeSafe or self-hosted). When set
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
	// Decider is set for the strands-decider provider: the server is the one
	// deciderserver finds or starts on loopback, and its URL is read from
	// the supervisor on every call.
	Decider *decisions.DeciderModel
	// CriteriaObject and MaxOptions shape requests for a Strands Decider
	// server (see decisions.SystemOne).
	CriteriaObject bool
	MaxOptions     int
	// WireModel, MaxQuestions, Envelope and ExtraHeaders shape requests for
	// Clef on Workers AI or AI Gateway (see decisions.SystemOne).
	WireModel    string
	MaxQuestions int
	// MaxBodyBytes caps a systemone request body (Tev1 on Ollama).
	MaxBodyBytes int
	Envelope     bool
	ExtraHeaders func() (map[string]string, error)
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
		s := &decisions.SystemOne{
			Name:           d.Provider,
			Model:          d.Model,
			SendModel:      d.SendModel,
			BaseURL:        d.BaseURL,
			Path:           d.Path,
			Key:            d.Key,
			Client:         client,
			Timeout:        d.Timeout,
			CriteriaObject: d.CriteriaObject,
			MaxOptions:     d.MaxOptions,
			WireModel:      d.WireModel,
			MaxQuestions:   d.MaxQuestions,
			MaxBodyBytes:   d.MaxBodyBytes,
			Envelope:       d.Envelope,
			ExtraHeaders:   d.ExtraHeaders,
		}
		if d.Decider != nil {
			sup := deciderserver.Shared(*d.Decider)
			s.Resolve = sup.URL
			s.OnConnRefused = sup.Recover
		}
		return s
	case decisions.BackendChatLetters:
		return &decisions.ChatLetters{
			Name:          d.Provider,
			BaseURL:       d.BaseURL,
			Model:         d.Model,
			Key:           d.Key,
			Client:        client,
			Timeout:       d.Timeout,
			MaxStateBytes: d.MaxStateBytes,
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
	if ps, ok := src.(ProviderSource); ok && cls.Provider != decisions.LocalProvider && cls.Provider != decisions.DeciderProvider {
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
	case decisions.BackendChatLetters:
		return resolveTev1(out, src)
	case decisions.BackendSystemOne:
		if decisions.IsHostedClef(cls.Provider, cls.Model) {
			return resolveClef(out, cls, src)
		}
		if decisions.IsOllamaTev1(cls.Provider, cls.Model) {
			return resolveOllamaTev1(out, src)
		}
		if cls.Provider == decisions.DeciderProvider {
			// Strands Decider-2B on this machine: a loopback server the
			// supervisor finds or starts, no key, no profile.
			m, ok := decisions.DeciderModelByID(cls.Model)
			if !ok {
				return DecisionsConfig{}, true, fmt.Errorf("classifier.model %q is not a Strands Decider model (want %s)", cls.Model, deciderModelIDs())
			}
			out.Model = m.ID
			out.Decider = &m
			out.BaseURL = deciderserver.BaseURL()
			out.Path = decisions.DefaultSystemOnePath
			out.CriteriaObject = true
			out.MaxOptions = m.MaxOptions
			return out, true, nil
		}
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
		// A profile serving Strands Decider (on another host, or a Hugging
		// Face Inference Endpoint) takes the decider's request shape.
		if n := decisions.DeciderMaxOptions(out.Model); n > 0 {
			out.CriteriaObject, out.MaxOptions = true, n
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

// cfAccountID is the shape of a Cloudflare account id.
var cfAccountID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// cfGatewayID is the shape of an AI Gateway id.
var cfGatewayID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// lookupCred reads one credential field, trimmed; "" when unset.
func lookupCred(src CredentialSource, provider, field string) string {
	if src == nil {
		return ""
	}
	v, _, _ := src.Lookup(provider, field)
	return strings.TrimSpace(v)
}

// resolveClef resolves Clef on Workers AI, directly or through AI Gateway,
// from the user's own Cloudflare credentials. It is never firewall-routed:
// the request is built here, not from a chat config. The key rides only in
// the Authorization header to Cloudflare's API (or the gateway), and the
// gateway token only in cf-aig-authorization to the gateway.
func resolveClef(out DecisionsConfig, cls *config.ClassifierSettings, src CredentialSource) (DecisionsConfig, bool, error) {
	m, _ := decisions.ClefByID(strings.TrimSpace(cls.Model))
	out.Model = m.ID
	out.WireModel = m.Wire
	out.SendModel = true
	out.Envelope = true
	out.CriteriaObject = true
	out.MaxOptions = decisions.ClefMaxOptions
	out.MaxQuestions = decisions.ClefMaxQuestions
	workersKey := func() (string, error) {
		return lookupCred(src, decisions.CloudflareWorkersAIProvider, "api_key"), nil
	}
	switch cls.Provider {
	case decisions.CloudflareWorkersAIProvider:
		acct := lookupCred(src, decisions.CloudflareWorkersAIProvider, "account_id")
		if !cfAccountID.MatchString(acct) {
			return DecisionsConfig{}, true, fmt.Errorf("cloudflare-workers-ai: account_id must be a Cloudflare account id (32 hex characters) to run %s", m.ID)
		}
		out.BaseURL = "https://api.cloudflare.com/client/v4/accounts/" + acct
		out.Path = "/ai/run/" + m.ID
		out.Key = func() (string, error) {
			k, _ := workersKey()
			if k == "" {
				return "", fmt.Errorf("cloudflare-workers-ai has no API token")
			}
			return k, nil
		}
	case decisions.CloudflareGatewayProvider:
		base, err := clefGatewayBase(lookupCred(src, decisions.CloudflareGatewayProvider, "base_url"), lookupCred(src, decisions.CloudflareGatewayProvider, "account_id"))
		if err != nil {
			return DecisionsConfig{}, true, err
		}
		out.BaseURL = base
		out.Path = "/workers-ai/" + m.ID
		// The Workers AI token when one is configured; otherwise the
		// gateway's stored key answers.
		out.Key = workersKey
		token := lookupCred(src, decisions.CloudflareGatewayProvider, "token")
		out.ExtraHeaders = func() (map[string]string, error) {
			if token == "" {
				return nil, nil
			}
			return map[string]string{"cf-aig-authorization": "Bearer " + token}, nil
		}
	}
	return out, true, nil
}

// resolveTev1 resolves Tev1 on Together from the user's own Together key. It
// is never firewall-routed: the request is built here, not from a chat
// config, and the key rides only in the Authorization header to Together's
// API (or the base_url the user set, https or loopback http).
func resolveTev1(out DecisionsConfig, src CredentialSource) (DecisionsConfig, bool, error) {
	out.Model = decisions.Tev1HostedModel
	base := lookupCred(src, decisions.TogetherProvider, "base_url")
	if base == "" {
		d, _ := provider.Lookup(decisions.TogetherProvider)
		base = d.BaseURL
	}
	if err := config.ValidJevURL(base); err != nil {
		return DecisionsConfig{}, true, fmt.Errorf("together: %w", err)
	}
	out.BaseURL = strings.TrimRight(base, "/")
	out.Key = func() (string, error) {
		k := lookupCred(src, decisions.TogetherProvider, "api_key")
		if k == "" {
			return "", fmt.Errorf("together has no API key (TOGETHER_API_KEY)")
		}
		return k, nil
	}
	return out, true, nil
}

// resolveOllamaTev1 resolves a Tev1 tag on Ollama, answered by Ollama's own
// /v1/systemone (Ollama 0.35 and later) at the address the ollama provider is
// configured with: https, or http on loopback.
func resolveOllamaTev1(out DecisionsConfig, src CredentialSource) (DecisionsConfig, bool, error) {
	fields := map[string]string{}
	for _, f := range []string{"host", "port", "protocol"} {
		fields[f] = lookupCred(src, decisions.OllamaProvider, f)
	}
	d, _ := provider.Lookup(decisions.OllamaProvider)
	base := d.BaseURL
	if d.BaseURLBuilder != nil {
		base = d.BaseURLBuilder(fields)
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	if err := config.ValidJevURL(base); err != nil {
		return DecisionsConfig{}, true, fmt.Errorf("ollama: %w", err)
	}
	out.BaseURL, out.Path = base, decisions.DefaultSystemOnePath
	out.Model = strings.ToLower(strings.TrimSpace(out.Model))
	out.SendModel = true
	out.MaxOptions = decisions.Tev1MaxOptions
	out.MaxQuestions = decisions.ClefMaxQuestions
	out.MaxBodyBytes = decisions.OllamaMaxBodyBytes
	out.Key = func() (string, error) {
		return lookupCred(src, decisions.OllamaProvider, "api_key"), nil
	}
	return out, true, nil
}

// clefGatewayBase is the AI Gateway root a Workers AI call goes under:
// https://gateway.ai.cloudflare.com/v1/{account}/{gateway}. A configured
// base_url (the compat URL the chat provider uses) names the gateway;
// otherwise it is the account's "default" gateway.
func clefGatewayBase(baseURL, account string) (string, error) {
	gateway := "default"
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || u.Scheme != "https" || u.Host != "gateway.ai.cloudflare.com" || u.User != nil {
			return "", fmt.Errorf("cloudflare-ai-gateway: base_url must be an https URL on gateway.ai.cloudflare.com to run Clef")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 3 || parts[0] != "v1" {
			return "", fmt.Errorf("cloudflare-ai-gateway: base_url must look like https://gateway.ai.cloudflare.com/v1/{account}/{gateway}/compat")
		}
		account, gateway = parts[1], parts[2]
	}
	if !cfAccountID.MatchString(account) {
		return "", fmt.Errorf("cloudflare-ai-gateway: account_id must be a Cloudflare account id (32 hex characters) to run Clef")
	}
	if !cfGatewayID.MatchString(gateway) {
		return "", fmt.Errorf("cloudflare-ai-gateway: the gateway id in base_url is not a plain name")
	}
	return "https://gateway.ai.cloudflare.com/v1/" + account + "/" + gateway, nil
}

func deciderModelIDs() string {
	ids := make([]string, 0, len(decisions.DeciderModels))
	for _, m := range decisions.DeciderModels {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, " or ")
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
	if d.Decider != nil {
		root, err := deciderserver.Root()
		if err != nil {
			return ""
		}
		if !deciderserver.Present(root, *d.Decider) {
			if _, _, ok := deciderserver.Running(context.Background(), deciderserver.Options{}); !ok {
				return deciderserver.Describe(deciderserver.ErrWeightsMissing)
			}
		}
		deciderserver.Shared(*d.Decider).Recover()
		return ""
	}
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

// ClefCredsOK reports whether the user has Cloudflare Workers AI credentials
// that could run Clef: a Cloudflare account id and an API token. It reads the
// credential source only and makes no request, so it says the credentials are
// set and well shaped, not that Cloudflare accepts them.
func ClefCredsOK(src CredentialSource) bool {
	return cfAccountID.MatchString(lookupCred(src, decisions.CloudflareWorkersAIProvider, "account_id")) &&
		lookupCred(src, decisions.CloudflareWorkersAIProvider, "api_key") != ""
}

// ClefDecider returns the decision backend for the built-in clef MCP server and
// for ask decisions. It is the user's classifier when that is a SystemOne
// backend (in a Pix Sandbox, Clef through the sandbox Worker); otherwise it is
// Clef-flash on Cloudflare Workers AI from the user's own credentials. A local
// decision model, OpenRouter's Decisions API and Tev1 on Together are not used:
// the first does not speak the SystemOne API the tools' questions need to be
// answered by a Clef-class head, and the others are not Clef. The request is
// built here and never firewall-routed (resolveClef).
func ClefDecider(cls *config.ClassifierSettings, src CredentialSource, client *http.Client) (decisions.Decider, error) {
	if cc, err := ResolveClassifier(Config{}, cls, src); err == nil && cc.Decisions.On() && cc.Decisions.Backend == decisions.BackendSystemOne {
		return cc.Decisions.NewDecider(client), nil
	}
	if !ClefCredsOK(src) {
		return nil, errors.New("no decision model is configured: set Cloudflare Workers AI credentials or a Clef or SystemOne classifier")
	}
	own := &config.ClassifierSettings{Provider: decisions.CloudflareWorkersAIProvider, Model: decisions.ClefModels[0].ID}
	cc, err := ResolveClassifier(Config{}, own, src)
	if err != nil || !cc.Decisions.On() {
		return nil, errors.New("Cloudflare Workers AI is not set up for decisions")
	}
	return cc.Decisions.NewDecider(client), nil
}
