package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
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
