package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/models"
)

// Local first: under kind systemone, Strands Decider-2B leads the provider
// list once it is detected on this machine, and is still offered (second)
// when it is not, so its test can say how to install it.
func TestDeciderLeadsSystemOneWhenDetected(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.settings.Classifier = &config.ClassifierSettings{Kind: config.SystemOneKind, Provider: decisions.TypeSafeProvider}

	if got := a.classifierProviders(); len(got) < 2 || got[0] != decisions.TypeSafeProvider || got[1] != decisions.DeciderProvider {
		t.Fatalf("undetected: providers = %v", got)
	}
	if !strings.Contains(a.renderDeciderRow(false), "not installed") {
		t.Errorf("row = %q", a.renderDeciderRow(false))
	}

	a.handleDeciderProbed(deciderProbedMsg{status: deciderserver.Status{Installed: true, Running: "http://127.0.0.1:18098", Device: "cpu"}, hubKnown: true})
	if got := a.classifierProviders(); got[0] != decisions.DeciderProvider {
		t.Fatalf("detected: providers = %v", got)
	}
	row := a.renderDeciderRow(true)
	if !strings.Contains(row, "local first") || !strings.Contains(row, "running at 127.0.0.1:18098 on cpu") {
		t.Errorf("row = %q", row)
	}
	cat := a.decisionCatalog(decisions.DeciderProvider)
	if len(cat) != 1 || cat[0].ID != "decider-2b" || !strings.Contains(cat[0].Label, "no inference provider serves it yet") {
		t.Errorf("catalog = %+v", cat)
	}
	if !a.providerIsDecisions(decisions.DeciderProvider) {
		t.Error("strands-decider must be a decision provider")
	}
	found := false
	for _, n := range a.providerViewNames() {
		found = found || n == decisions.DeciderProvider
	}
	if !found {
		t.Error("/providers does not list strands-decider")
	}
}

// Hosted Clef leads the systemone kind once Cloudflare is configured, its
// picker offers Clef only, and no chat picker offers a Clef model.
func TestHostedClefLeadsSystemOneWhenConfigured(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_API_KEY", "cf-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	a := newModelScreen(t, t.TempDir())
	a.resolver = newTestResolver(t, a.workdir)
	if !a.cloudflareClefReady(decisions.CloudflareWorkersAIProvider) {
		t.Fatal("cloudflare-workers-ai is not configured from the environment")
	}
	a.settings.Classifier = &config.ClassifierSettings{Kind: config.SystemOneKind, Provider: decisions.TypeSafeProvider}
	if got := a.classifierProviders(); got[0] != decisions.CloudflareWorkersAIProvider {
		t.Fatalf("providers = %v, want cloudflare-workers-ai first", got)
	}
	a.modelState.pickingRole = roleClassifier
	a.modelState.picking = true
	if a.modelState.pendingProvider == nil {
		a.modelState.pendingProvider = map[modelRole]string{}
	}
	a.modelState.pendingProvider[roleClassifier] = decisions.CloudflareWorkersAIProvider
	name, cat := a.modelPickerCatalog()
	if name != decisions.CloudflareWorkersAIProvider || len(cat) != len(decisions.ClefModels) {
		t.Fatalf("picker %s %+v", name, cat)
	}
	for _, m := range cat {
		if _, ok := decisions.ClefByID(m.ID); !ok {
			t.Fatalf("the systemone picker offered %s", m.ID)
		}
	}
	chat := filterOutDecisionsModels(decisions.CloudflareWorkersAIProvider, []models.Model{{ID: "@cf/cloudflare/clef"}, {ID: "@cf/moonshotai/kimi-k2.6"}})
	if len(chat) != 1 || chat[0].ID != "@cf/moonshotai/kimi-k2.6" {
		t.Fatalf("chat catalogue = %v", chat)
	}
}

func TestTev1OfferedWhenItsProviderIsConfigured(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TOGETHER_API_KEY", "")
	a := newModelScreen(t, t.TempDir())
	a.resolver = newTestResolver(t, a.workdir)
	a.settings.Classifier = &config.ClassifierSettings{Kind: config.SystemOneKind, Provider: decisions.TypeSafeProvider}
	for _, p := range a.classifierProviders() {
		if p == decisions.TogetherProvider || p == decisions.OllamaProvider {
			t.Fatalf("%s offered for Tev1 without being configured: %v", p, a.classifierProviders())
		}
	}

	t.Setenv("TOGETHER_API_KEY", "tg-key")
	a.resolver = newTestResolver(t, a.workdir)
	if got := a.classifierProviders(); indexOfString(got, decisions.TogetherProvider) < 0 || indexOfString(got, decisions.TogetherProvider) > indexOfString(got, decisions.TypeSafeProvider) {
		t.Fatalf("providers = %v, want together before typesafe", got)
	}
	a.modelState.pickingRole = roleClassifier
	a.modelState.picking = true
	if a.modelState.pendingProvider == nil {
		a.modelState.pendingProvider = map[modelRole]string{}
	}
	a.modelState.pendingProvider[roleClassifier] = decisions.TogetherProvider
	if name, cat := a.modelPickerCatalog(); name != decisions.TogetherProvider || len(cat) != 1 || cat[0].ID != decisions.Tev1HostedModel {
		t.Fatalf("picker %s %+v", name, cat)
	}

	// Ollama is offered once it lists a Tev1 tag, and only its Tev1 tags.
	a.catalogCache[decisions.OllamaProvider] = []models.Model{{ID: "llama3"}, {ID: "tev1:4b"}}
	if indexOfString(a.classifierProviders(), decisions.OllamaProvider) < 0 {
		t.Fatalf("ollama with tev1:4b not offered: %v", a.classifierProviders())
	}
	a.modelState.pendingProvider[roleClassifier] = decisions.OllamaProvider
	if _, cat := a.modelPickerCatalog(); len(cat) != 1 || cat[0].ID != "tev1:4b" {
		t.Fatalf("ollama picker %+v", cat)
	}
	chat := filterOutDecisionsModels(decisions.OllamaProvider, a.catalogCache[decisions.OllamaProvider])
	if len(chat) != 1 || chat[0].ID != "llama3" {
		t.Fatalf("a Tev1 tag must never be a chat model: %v", chat)
	}
	if !a.classifierSelectsDecision(&config.ClassifierSettings{Provider: decisions.TogetherProvider, Model: decisions.Tev1HostedModel}) {
		t.Fatal("Tev1 on Together is a decision selection")
	}
}
