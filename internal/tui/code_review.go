package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/review"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
)

// /review (docs/review.md): a code review turn. The harness reads the change
// and applies the caps, a read-only model turn judges it, and the harness
// validates what comes back. The result is shown in the thread as a row that
// only the screen holds and is kept in the review store; it never joins the
// conversation. Posting it to a pull request is a separate, confirmed step.

// codeReviewState is the review in flight and the last result. The UI loop
// owns it.
type codeReviewState struct {
	busy bool
	last *review.Result
	ask  codeReviewAsk
	seq  int

	// reviewer and provider replace the real ones in tests.
	reviewer review.Reviewer
	provider func() (forge.Provider, error)
	store    *review.Store
}

// codeReviewAsk is the open question "post this review?".
type codeReviewAsk struct {
	seq     int
	res     *review.Result
	working bool
	err     string
}

// codeReviewDoneMsg is a finished review. sessionID is the session it was
// started in; a result for a session the user has left is dropped.
type codeReviewDoneMsg struct {
	sessionID string
	res       review.Result
	err       error
}

// codeReviewPostedMsg is the outcome of a confirmed post.
type codeReviewPostedMsg struct {
	seq int
	pr  int
	err error
}

const codeReviewUsage = "usage: /review [pr [N]] [branch [NAME]] [--base REF] [force]  reviews the working tree, a pull request or a branch; /review post posts the last pull request review after you confirm it"

// reviewRequest is a parsed /review line.
type reviewRequest struct {
	target review.Target
	force  bool
	post   bool
}

// parseReviewRequest reads the arguments of /review. It returns the reason in
// words when they do not parse.
func parseReviewRequest(arg string) (reviewRequest, string) {
	var q reviewRequest
	f := strings.Fields(arg)
	for i := 0; i < len(f); i++ {
		switch strings.ToLower(f[i]) {
		case "force", "--force":
			q.force = true
		case "post":
			q.post = true
		case "pr":
			q.target.Kind = review.KindPR
			if i+1 < len(f) && (f[i+1] == "current" || isNumber(strings.TrimPrefix(f[i+1], "#"))) {
				i++
				q.target.Ref = f[i]
			}
		case "branch":
			q.target.Kind = review.KindBranch
			if i+1 < len(f) && !strings.HasPrefix(f[i+1], "-") && f[i+1] != "force" {
				i++
				q.target.Ref = f[i]
			}
		case "--base":
			if i+1 >= len(f) {
				return q, "--base needs a ref"
			}
			i++
			q.target.Base = f[i]
			if q.target.Kind == "" {
				q.target.Kind = review.KindBranch
			}
		default:
			return q, "not understood: " + sanitize.Line(f[i], 40)
		}
	}
	switch {
	case q.post && (q.target.Kind != "" || q.force):
		return q, "post takes no other argument"
	case q.target.Kind == review.KindPR && q.target.Base != "":
		return q, "a pull request has its own base, so --base does not apply"
	}
	return q, ""
}

func isNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// codeReviewRefusal says why /review cannot start now, or "" when it can.
func (a *App) codeReviewRefusal() string {
	switch {
	case !a.settings.ReviewEnabled():
		return "off (review.enabled is false)"
	case a.codeReview.busy:
		return "a review is already running"
	case a.codeReview.reviewer == nil && !a.status.Configured:
		return "no model is configured, so there is nothing to review with"
	}
	return ""
}

func (a *App) codeReviewPerms() permissions.Settings {
	return permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
}

func (a *App) codeReviewProvider() func() (forge.Provider, error) {
	if a.codeReview.provider != nil {
		return a.codeReview.provider
	}
	dir := a.workdir
	return func() (forge.Provider, error) { return review.ProviderFor(dir) }
}

func (a *App) codeReviewStore() *review.Store {
	if a.codeReview.store != nil {
		return a.codeReview.store
	}
	s, err := review.OpenStore()
	if err != nil {
		return nil
	}
	return s
}

func (a *App) codeReviewGuard() review.Guard {
	return review.Guard{Perms: a.codeReviewPerms(), Hooks: a.sessionHooks(), SessionID: a.sessionID, Cwd: a.workdir}
}

// codeReviewCommand handles /review. It is the user's alone: no model can start it,
// and nothing it shows or stores reaches a model.
func (a *App) codeReviewCommand(arg string) tea.Cmd {
	q, why := parseReviewRequest(arg)
	if why != "" {
		a.addEphemeralSystem("review: " + why + "\n" + codeReviewUsage)
		return nil
	}
	if q.post {
		return a.codeReviewPostCommand()
	}
	if why := a.codeReviewRefusal(); why != "" {
		a.addEphemeralSystem("review: " + why)
		return nil
	}
	provider := a.codeReviewProvider()
	reviewer := a.codeReview.reviewer
	if reviewer == nil {
		mgr := bgagent.NewManager(a.workdir, a.cfg, a.client, a.settings, a.effectivePosture())
		mgr.SetCredentialSource(credentialSourceOf(a.resolver))
		mgr.SetPool(a.agentPool)
		mgr.SetSessionID(a.sessionID)
		reviewer = review.Agent{Manager: mgr, Workdir: a.workdir}
	}
	opts := review.Options{
		Settings: a.settings,
		Collector: review.Collector{
			Dir:      a.workdir,
			Hide:     a.diffHide(),
			Provider: func(context.Context) (forge.Provider, error) { return provider() },
		},
		Reviewer: reviewer,
		Store:    a.codeReviewStore(),
		Rubric:   review.LoadRubric(a.settings, a.workdir),
		Force:    q.force,
	}
	target, sid := q.target, a.sessionID
	a.codeReview.busy = true
	a.addEphemeralSystem("review: reading the change and asking the model, read-only; this can take a few minutes (nothing is added to the conversation)")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), review.Timeout+2*time.Minute)
		defer cancel()
		res, _, err := review.Run(ctx, opts, target)
		return codeReviewDoneMsg{sessionID: sid, res: res, err: err}
	}
}

// handleCodeReviewDone shows a finished review.
func (a *App) handleCodeReviewDone(m codeReviewDoneMsg) tea.Cmd {
	a.codeReview.busy = false
	if m.sessionID != a.sessionID {
		return nil
	}
	if m.err != nil {
		a.addEphemeralSystem("review: " + sanitize.Line(m.err.Error(), 400))
		return nil
	}
	res := m.res
	a.codeReview.last = &res
	text := review.Format(res)
	if res.Target.Kind == review.KindPR && a.settings.ReviewPostMode() != config.ReviewPostDeny && !res.Incomplete {
		text += "post it to the pull request with /review post (you confirm first)\n"
	}
	a.addEphemeralSystem(strings.TrimRight(text, "\n"))
	return nil
}

// codeReviewPostCommand opens the question for the last pull request review.
func (a *App) codeReviewPostCommand() tea.Cmd {
	res := a.codeReview.last
	switch {
	case a.settings.ReviewPostMode() == config.ReviewPostDeny:
		a.addEphemeralSystem("review: posting is off (review.post is deny)")
	case res == nil || res.Target.Kind != review.KindPR:
		a.addEphemeralSystem("review: run /review pr first; only the review of a pull request can be posted")
	case res.Incomplete:
		a.addEphemeralSystem("review: the last review has no readable verdict, so it is not posted")
	case a.viewOpen(viewReviewPost) || a.view != viewChat:
		a.addEphemeralSystem("review: another screen is open; close it and run /review post again")
	default:
		a.codeReview.ask = codeReviewAsk{seq: a.codeReview.ask.seq + 1, res: res}
		return a.push(viewReviewPost)
	}
	return nil
}

func (a *App) closeCodeReviewPost(reason string) {
	open := a.viewOpen(viewReviewPost)
	a.codeReview.ask = codeReviewAsk{seq: a.codeReview.ask.seq + 1}
	if open {
		a.dropView(viewReviewPost)
	}
	if reason != "" {
		a.addEphemeralSystem(reason)
	}
}

func (a *App) codeReviewPostView() string {
	w := a.contentWidth()
	st := a.codeReview.ask
	var b strings.Builder
	b.WriteString(components.SectionHeader("Post review", "esc deny", w))
	b.WriteString("\n")
	if st.res == nil {
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}
	b.WriteString(components.EmphStyle.Render(fmt.Sprintf("this comment will be posted on pull request #%d", st.res.PR)) + "\n\n")
	lines := strings.Split(review.Comment(*st.res), "\n")
	for i, l := range lines {
		if i == 28 {
			b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  ... %d more line(s)", len(lines)-i)) + "\n")
			break
		}
		b.WriteString("  " + sanitize.Line(l, max(20, w-6)) + "\n")
	}
	b.WriteString("\n" + components.MutedStyle.Render("an advisory comment: it does not approve, block or merge anything") + "\n")
	if st.err != "" {
		b.WriteString("\n" + components.DangerStyle.Render("x "+oneLine(st.err, w)) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("y", "post", "n", "deny", "esc", "deny") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleCodeReviewPostKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.codeReview.ask
	if st.res == nil {
		a.pop()
		return a, nil
	}
	switch m.String() {
	case "esc", "n", "q":
		a.closeCodeReviewPost("review: not posted")
	case "y":
		if st.working {
			return a, nil
		}
		st.working = true
		res, seq := *st.res, st.seq
		req := review.PostRequest{
			Result: res, Dir: a.workdir, Store: a.codeReviewStore(), Guard: a.codeReviewGuard(),
			Mode: a.settings.ReviewPostMode(), Confirmed: true,
		}
		provider := a.codeReviewProvider()
		return a, func() tea.Msg {
			p, err := provider()
			if err != nil {
				return codeReviewPostedMsg{seq: seq, pr: res.PR, err: err}
			}
			req.Provider = p
			ctx, cancel := context.WithTimeout(context.Background(), forge.WriteTimeout+10*time.Second)
			defer cancel()
			return codeReviewPostedMsg{seq: seq, pr: res.PR, err: review.Post(ctx, req)}
		}
	}
	return a, nil
}

// handleCodeReviewPosted applies the outcome of a confirmed post.
func (a *App) handleCodeReviewPosted(m codeReviewPostedMsg) tea.Cmd {
	if m.seq != a.codeReview.ask.seq {
		return nil
	}
	if m.err != nil {
		a.codeReview.ask.working = false
		a.codeReview.ask.err = sanitize.Line(m.err.Error(), 300)
		return nil
	}
	a.closeCodeReviewPost(fmt.Sprintf("review: posted to pull request #%d", m.pr))
	return nil
}
