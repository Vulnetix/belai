package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentdraft"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// ── Web agent drafts ─────────────────────────────────────────────────────
//
// The website's agent builder asks this live session to draft an agent
// profile from a premise (vdb-site handler/belai_drafts.go). The premise is
// untrusted web text, so it takes the web-prompt path's checks: it arrives
// only while sync.remote_prompts is on, it is cleaned by CleanPrompt, and it
// is admitted through the security classifier under the effective posture
// before the drafter sees it. The drafter is a tool-less classifier call
// (internal/agentdraft). Nothing is written on this host: the offers go back
// to the website, where the user takes, edits or drops each one and downloads
// the file. The reason sent for a refusal is always harness text.

// remoteDraftMsg carries one draft request claimed from the inbox.
type remoteDraftMsg sessionsync.RemoteDraft

// remoteDraftDoneMsg reports a finished draft to the transcript.
type remoteDraftDoneMsg struct {
	fields int
	reason string
}

// watchRemoteDrafts waits for the next draft request.
func (a *App) watchRemoteDrafts() tea.Cmd {
	if a.syncer == nil || !a.syncer.RemotePromptsEnabled() {
		return nil
	}
	ch := a.syncer.Drafts()
	return func() tea.Msg {
		d, ok := <-ch
		if !ok {
			return nil
		}
		return remoteDraftMsg(d)
	}
}

// Draft time bounds: finish a little before the website gives up, never
// drag a stuck model call on past a few minutes, never start with no time.
const (
	draftMargin     = 5 * time.Second
	draftMinTimeout = 10 * time.Second
	draftMaxTimeout = 150 * time.Second
)

// draftTimeout is how long a draft may run, from its expiry.
func draftTimeout(expiresAtMs int64, now time.Time) time.Duration {
	left := time.UnixMilli(expiresAtMs).Sub(now) - draftMargin
	switch {
	case left < draftMinTimeout:
		return draftMinTimeout
	case left > draftMaxTimeout:
		return draftMaxTimeout
	}
	return left
}

// handleRemoteDraft checks one draft request and starts it, or refuses it.
func (a *App) handleRemoteDraft(d sessionsync.RemoteDraft) tea.Cmd {
	if a.syncer == nil {
		return nil
	}
	syncer := a.syncer
	refuse := func(reason string) tea.Cmd {
		syncer.DraftResult(d.ID, sessionsync.DraftRefused, reason, nil)
		a.addSystem("web: an agent draft was refused: " + reason)
		return nil
	}
	if d.SessionID != a.sessionID {
		return refuse("the session is no longer active on the host")
	}
	premise := sessionsync.CleanPrompt(d.Premise)
	if premise == "" {
		return refuse("the premise was empty after cleaning")
	}
	if a.classifier == nil {
		return refuse(draftRefusalReason(agentdraft.ErrNoClassifier))
	}
	pol := a.effectivePosture()
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	drafter := agentdraft.Drafter{
		Classifier: a.classifier,
		Caveman:    a.settings.ClassifierCavemanEnabled(),
		Workers: func() []agentdraft.Worker {
			ps, _ := agentprofile.List()
			return agentdraft.WorkersFrom(ps)
		},
		Crews: agentprofile.ListCrews,
	}
	req := agentdraft.Request{Premise: premise, Workers: d.Context.Workers, Labels: d.Context.Labels}
	timeout := draftTimeout(d.ExpiresAt, time.Now())
	a.addSystem("web: drafting an agent for the website's agent builder")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		admit := func(ctx context.Context, text string) error {
			_, err := pipe.Admit(ctx, text, "web agent draft", pol)
			return err
		}
		res, reason := draftForWeb(ctx, admit, &drafter, req)
		if reason != "" {
			syncer.DraftResult(d.ID, sessionsync.DraftRefused, reason, nil)
			return remoteDraftDoneMsg{reason: reason}
		}
		syncer.DraftResult(d.ID, sessionsync.DraftDone, "", res)
		return remoteDraftDoneMsg{fields: len(res.Fields)}
	}
}

// drafter is what draftForWeb needs of agentdraft.Drafter.
type drafter interface {
	Draft(context.Context, agentdraft.Request) (agentdraft.Result, error)
}

// draftForWeb admits the premise, then drafts. reason is non-empty when the
// draft is refused; it is harness text, never model or provider output.
func draftForWeb(ctx context.Context, admit func(context.Context, string) error, d drafter, req agentdraft.Request) (agentdraft.Result, string) {
	if err := admit(ctx, req.Premise); err != nil {
		var refusal *rolemanager.RefusalError
		if errors.As(err, &refusal) {
			return agentdraft.Result{}, "the premise was refused by the security classifier (" + refusal.Sentinel.Label() + ")"
		}
		return agentdraft.Result{}, "the premise could not be checked by the security classifier"
	}
	res, err := d.Draft(ctx, req)
	if err != nil {
		return agentdraft.Result{}, draftRefusalReason(err)
	}
	return res, ""
}

// draftRefusalReason maps a drafter error to the reason the website shows.
func draftRefusalReason(err error) string {
	switch {
	case errors.Is(err, agentdraft.ErrNoClassifier):
		return "no classifier model is configured on this host"
	case errors.Is(err, agentdraft.ErrEmptyPremise):
		return "the premise was empty after cleaning"
	case errors.Is(err, agentdraft.ErrPremiseTooLong):
		return fmt.Sprintf("the premise is over %d characters", agentdraft.MaxPremiseChars)
	case errors.Is(err, context.DeadlineExceeded):
		return "the draft took too long"
	case errors.Is(err, agentdraft.ErrModelCall):
		return "the host's classifier model did not answer"
	default:
		return "the model did not produce a usable draft; try rewording the premise"
	}
}

// remoteDraftDone notes the outcome in the transcript.
func (a *App) remoteDraftDone(m remoteDraftDoneMsg) {
	if m.reason != "" {
		a.addSystem("web: an agent draft was refused: " + m.reason)
		return
	}
	a.addSystem(fmt.Sprintf("web: drafted an agent (%d fields) for the website's agent builder", m.fields))
}
