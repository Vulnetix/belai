package clefmcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/run"
)

// Engine is the decision service behind the clef MCP server and behind ask
// decisions: it knows whether the service is offered, finds the decision
// backend on each call (so a settings change or a late login takes effect
// without a restart) and answers a choice or a proposition with probabilities.
//
// The same engine serves a model that calls mcp__clef__* directly and the
// harness answering an ask the user would otherwise get. Neither path is a
// Jev job: the switches are the user's own mcp.builtin.clef settings.
type Engine struct {
	// Settings returns the current effective settings.
	Settings func() config.Settings
	// Creds returns the credential source, resolved afresh so a login is seen.
	Creds func() run.CredentialSource
	// Client reaches the backend. nil means httpclient.Default().
	Client func() *http.Client
	// Backend, when set, replaces the resolved decision backend (tests).
	Backend func() (decisions.Decider, error)
}

// Offered reports whether the decision server is offered now and, when it is
// not, why. It is offered when the user has not switched it off and a decision
// backend can serve it: the Pix Sandbox build always has one (its Worker), any
// other build needs the user's own Cloudflare Workers AI credentials.
func (e *Engine) Offered() (bool, string) {
	s := config.Settings{}
	if e.Settings != nil {
		s = e.Settings()
	}
	if !s.ClefMCPEnabled() {
		return false, "switched off in /model"
	}
	if SandboxBuild {
		return true, ""
	}
	var src run.CredentialSource
	if e.Creds != nil {
		src = e.Creds()
	}
	if run.ClefCredsOK(src) {
		return true, ""
	}
	return false, "needs Cloudflare Workers AI credentials (an account id and an API token)"
}

// AskPolicy returns the confidence above which a decision answers an ask in the
// user's place, and whether decisions answer asks at all (the server is offered
// and skip_ask is on).
func (e *Engine) AskPolicy() (threshold float64, on bool) {
	s := config.Settings{}
	if e.Settings != nil {
		s = e.Settings()
	}
	threshold, on = s.ClefSkipAsk()
	if !on {
		return threshold, false
	}
	ok, _ := e.Offered()
	return threshold, ok
}

// decider resolves the backend for one call.
func (e *Engine) decider() (decisions.Decider, error) {
	if e.Backend != nil {
		return e.Backend()
	}
	s := config.Settings{}
	if e.Settings != nil {
		s = e.Settings()
	}
	var src run.CredentialSource
	if e.Creds != nil {
		src = e.Creds()
	}
	client := httpclient.Default()
	if e.Client != nil {
		client = e.Client()
	}
	d, err := run.ClefDecider(s.Classifier, src, client)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Handler is the MCP server (initialize, ping, tools/list, tools/call).
func (e *Engine) Handler() jsonrpc.Handler { return New(e.decider).Handler() }

// Choose scores options for question: one weight per option in the order given,
// summing to 1, each rounded to four places. descriptions explain options by
// their text. Every string is cleaned for a decision request first. Any failure
// is an error and the caller asks the user.
func (e *Engine) Choose(ctx context.Context, question, context string, options []string, descriptions map[string]string) ([]float64, error) {
	_, w, err := New(e.decider).choice(ctx, enumArgs{base: base{Question: question, Context: context}, Options: options, Descriptions: descriptions})
	return w, err
}

// Judge returns P(true) of proposition, rounded to four places.
func (e *Engine) Judge(ctx context.Context, proposition, context string) (float64, error) {
	if err := needQuestion(proposition); err != nil {
		return 0, err
	}
	ans, err := New(e.decider).ask(ctx, stateFor(context), map[string]decisions.Question{"q0": decisions.Noul(text(proposition, maxQuestionRunes))})
	if err != nil {
		return 0, err
	}
	a, ok := ans["q0"]
	if !ok {
		return 0, errors.New("the decision model gave no answer")
	}
	return pBool(a), nil
}
